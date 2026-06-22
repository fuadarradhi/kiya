package kiya

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

var (
	rePostgresPlaceholder = regexp.MustCompile(`('(?:[^']|'')*')|(\$\$.*?\$\$)|(\?)`)
	reLogPlaceholder      = regexp.MustCompile(`('(?:[^']|'')*')|(\?)`)
	reValidIdentifier     = regexp.MustCompile(`^[a-zA-Z0-9_\.]+$`)
	reValidOrderBy        = regexp.MustCompile(`^[a-zA-Z0-9_\.\s,]+$`)
)

type Dialect interface {
	Name() string
	ConvertPlaceholders(query string) string
}

type MySQLDialect struct{}

func (d MySQLDialect) Name() string { return "mysql" }
func (d MySQLDialect) ConvertPlaceholders(query string) string {
	return query
}

type PostgresDialect struct{}

func (d PostgresDialect) Name() string { return "postgres" }
func (d PostgresDialect) ConvertPlaceholders(query string) string {
	i := 0
	return rePostgresPlaceholder.ReplaceAllStringFunc(query, func(match string) string {
		if strings.HasPrefix(match, "'") || strings.HasPrefix(match, "$") {
			return match
		}
		i++
		return fmt.Sprintf("$%d", i)
	})
}

type Result interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}

type insertResult struct {
	lastInsertId int64
	rowsAffected int64
}

func (r *insertResult) LastInsertId() (int64, error) { return r.lastInsertId, nil }
func (r *insertResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

type Tx interface {
	Executor
	Commit() error
	Rollback() error
}

type Executor interface {
	Select(ctx context.Context, dest any, query string, args ...any) error
	Get(ctx context.Context, dest any, query string, args ...any) error
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	Begin(ctx context.Context) (Tx, error)
}

type QueryLog struct {
	Query     string
	FullQuery string
	Args      []any
	Duration  time.Duration
	Err       error
	Rows      int64
	Context   map[string]any
}

type QueryLogger interface {
	Log(q QueryLog)
}

type frameworkLogger struct{}

func (l *frameworkLogger) Log(q QueryLog) {
	displayQuery := q.FullQuery
	if displayQuery == "" {
		displayQuery = q.Query
	}

	if q.Err != nil {
		LogError("[DB] %s | Args: %v | Error: %v | Duration: %s", q.Query, q.Args, q.Err, q.Duration)
	} else {
		LogInfo("[DB] %s | Rows: %d | Duration: %s", displayQuery, q.Rows, q.Duration)
	}
}

type ctxKey string

const RequestIDKey ctxKey = "request_id"

type DB struct {
	dialect          Dialect
	executor         Executor
	logger           QueryLogger
	closeFunc        func() error
	ctx              context.Context
	defaultCondition DefaultConditionFunc
}

func NewDB(exec Executor, dialect Dialect, logger QueryLogger, closeFunc func() error, defaultCondition DefaultConditionFunc) *DB {
	if logger != nil {
		exec = &LoggedExecutor{inner: exec, logger: logger}
	}
	return &DB{executor: exec, dialect: dialect, logger: logger, closeFunc: closeFunc, defaultCondition: defaultCondition}
}

func (db *DB) WithContext(ctx context.Context) *DB {
	return &DB{
		dialect:          db.dialect,
		executor:         db.executor,
		logger:           db.logger,
		closeFunc:        nil,
		ctx:              ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) Use(tx Tx) *DB {
	return &DB{
		dialect:          db.dialect,
		executor:         tx,
		logger:           db.logger,
		closeFunc:        nil,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func NewDatabase(cfg DatabaseConfig) (*DB, error) {
	if !cfg.Enabled {
		LogInfo("[Goserver] Database is disabled via config")
		return nil, nil
	}

	tz := cfg.Timezone
	if tz == "" {
		tz = "UTC"
	}

	host := cfg.Host
	if host == "" {
		host = "localhost"
	}

	var dsn string
	var dialect Dialect
	var driverName string

	switch cfg.Driver {
	case "mysql":
		dialect = MySQLDialect{}
		driverName = "mysql"
		locParam := strings.ReplaceAll(tz, "/", "%2F")
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=%s",
			cfg.User,
			cfg.Password,
			host,
			cfg.Port,
			cfg.Name,
			locParam,
		)

	case "postgres":
		dialect = PostgresDialect{}
		driverName = "pgx"

		sslMode := "require"
		if host == "localhost" || host == "127.0.0.1" {
			sslMode = "disable"
		}

		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
			host, cfg.Port, cfg.User, cfg.Password, cfg.Name, sslMode, tz)

	default:
		return nil, fmt.Errorf("driver database tidak didukung: '%s'. Hanya 'mysql' atau 'postgres' yang tersedia", cfg.Driver)
	}

	var db *sqlx.DB
	var err error

	db, err = sqlx.Connect(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("gagal membuat koneksi database (%s): %w", cfg.Driver, err)
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 50
	}
	db.SetMaxOpenConns(maxOpen)

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	db.SetMaxIdleConns(maxIdle)

	connMaxLifetime := cfg.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 5 * time.Minute
	}
	db.SetConnMaxLifetime(connMaxLifetime)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("gagal melakukan ping ke database (%s): %w", cfg.Driver, err)
	}

	LogInfo("[Goserver] Database connected | Driver: %s | Host: %s:%s | DB: %s | TZ: %s", cfg.Driver, host, cfg.Port, cfg.Name, tz)

	exec := &sqlxExecutor{db: db}

	return NewDB(exec, dialect, &frameworkLogger{}, db.Close, cfg.DefaultCondition), nil
}

func (db *DB) Close() error {
	if db.closeFunc != nil {
		return db.closeFunc()
	}
	return nil
}

func (db *DB) Insert(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("Insert: %w", err)
	}
	return db.Table(tableName).Insert(data)
}

func (db *DB) Update(data any) (Result, error) {
	return nil, errors.New("Update requires a Where clause. Use db.Where(...).Update() or db.UpdateAll()")
}

func (db *DB) UpdateAll(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("UpdateAll: %w", err)
	}
	return db.Table(tableName).execUpdate(data, true)
}

func (db *DB) Delete(data any) (Result, error) {
	return nil, errors.New("Delete requires a Where clause. Use db.Where(...).Delete() or db.DeleteAll()")
}

func (db *DB) DeleteAll(data any) (Result, error) {
	tableName, err := getTableNameFromModel(data)
	if err != nil {
		return nil, fmt.Errorf("DeleteAll: %w", err)
	}
	return db.Table(tableName).execDelete(data, true)
}

func (db *DB) Find(dest any) (bool, error) {
	tableName, err := getTableNameFromModel(dest)
	if err != nil {
		return false, fmt.Errorf("Find: %w", err)
	}
	return db.Table(tableName).Find(dest)
}

func (db *DB) FindAll(dest any) error {
	tableName, err := getTableNameFromModel(dest)
	if err != nil {
		return fmt.Errorf("FindAll: %w", err)
	}
	return db.Table(tableName).FindAll(dest)
}

func (db *DB) Table(name string) *Builder {
	safeName := sanitizeIdentifier(name)
	if safeName == "" && name != "" {
		LogError("[DB Security] Invalid table name blocked: %s", name)
	}
	return &Builder{
		table:            safeName,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) Where(expr string, args ...any) *Builder {
	return db.Table("").Where(expr, args...)
}

func (db *DB) Raw(query string, args ...any) *Builder {
	return &Builder{
		rawQuery:         query,
		rawArgs:          args,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) NamedRaw(query string, args map[string]any) *Builder {
	return &Builder{
		rawQuery:         query,
		namedArgs:        args,
		dialect:          db.dialect,
		executor:         db.executor,
		ctx:              db.ctx,
		defaultCondition: db.defaultCondition,
	}
}

func (db *DB) Begin(ctx context.Context) (Tx, error) {
	if ctx == nil && db.ctx != nil {
		ctx = db.ctx
	}
	return db.executor.Begin(ctx)
}

func (db *DB) Transaction(ctx context.Context, fn func(tx Tx) error) (err error) {
	if ctx == nil && db.ctx != nil {
		ctx = db.ctx
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			LogError("[DB] Transaction rollback error: %v", rbErr)
		}
		return err
	}

	return tx.Commit()
}

func scanMapToStruct(data map[string]interface{}, dest interface{}) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr {
		return errors.New("destination must be a pointer")
	}
	if destVal.IsNil() {
		return errors.New("destination pointer is nil")
	}

	destVal = destVal.Elem()
	if destVal.Kind() != reflect.Struct {
		return errors.New("destination must point to a struct")
	}

	info, err := getStructInfo(destVal.Type())
	if err != nil {
		return err
	}

	for _, f := range info.fields {
		val, ok := data[f.name]

		if !ok || val == nil {
			continue
		}

		fieldVal := destVal.Field(f.idx)
		if !fieldVal.CanSet() {
			continue
		}

		setFieldValue(fieldVal, val)
	}

	return nil
}

func setFieldValue(field reflect.Value, value interface{}) {
	if value == nil {
		return
	}

	val := reflect.ValueOf(value)

	switch field.Kind() {
	case reflect.String:
		if val.Kind() == reflect.Slice && val.Type().Elem().Kind() == reflect.Uint8 {
			field.SetString(string(value.([]byte)))
			return
		}
		if val.Kind() == reflect.String {
			field.SetString(val.String())
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if val.Kind() == reflect.Int64 {
			field.SetInt(val.Int())
		} else if val.Kind() == reflect.Float64 {
			field.SetInt(int64(val.Float()))
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if val.Kind() == reflect.Int64 {
			field.SetUint(uint64(val.Int()))
		} else if val.Kind() == reflect.Float64 {
			field.SetUint(uint64(val.Float()))
		}

	case reflect.Float32, reflect.Float64:
		if val.Kind() == reflect.Float64 {
			field.SetFloat(val.Float())
		}

	case reflect.Bool:
		if val.Kind() == reflect.Int64 {
			field.SetBool(val.Int() != 0)
		} else if val.Kind() == reflect.Bool {
			field.SetBool(val.Bool())
		}

	case reflect.Struct:
		if val.Type() == field.Type() {
			field.Set(val)
		}

	default:
		if val.Type().ConvertibleTo(field.Type()) {
			field.Set(val.Convert(field.Type()))
		}
	}
}

type sqlxExecutor struct {
	db *sqlx.DB
}

func (e *sqlxExecutor) Select(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Slice {
		return e.db.SelectContext(ctx, dest, query, args...)
	}

	rows, err := e.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	sliceVal := destVal.Elem()
	isPtr := sliceVal.Type().Elem().Kind() == reflect.Ptr
	baseType := sliceVal.Type().Elem()
	if isPtr {
		baseType = baseType.Elem()
	}

	for rows.Next() {
		results := make(map[string]interface{})
		if err := rows.MapScan(results); err != nil {
			return err
		}

		newItem := reflect.New(baseType)

		if err := scanMapToStruct(results, newItem.Interface()); err != nil {
			return err
		}

		if isPtr {
			sliceVal.Set(reflect.Append(sliceVal, newItem))
		} else {
			sliceVal.Set(reflect.Append(sliceVal, newItem.Elem()))
		}
	}

	return rows.Err()
}

func (e *sqlxExecutor) Get(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Struct {
		return e.db.GetContext(ctx, dest, query, args...)
	}

	rows, err := e.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		return sql.ErrNoRows
	}

	results := make(map[string]interface{})
	if err := rows.MapScan(results); err != nil {
		return err
	}

	return scanMapToStruct(results, dest)
}

func (e *sqlxExecutor) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	return e.db.ExecContext(ctx, query, args...)
}

func (e *sqlxExecutor) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlxTx{tx: tx}, nil
}

type sqlxTx struct {
	tx *sqlx.Tx
}

func (t *sqlxTx) Select(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Slice {
		return t.tx.SelectContext(ctx, dest, query, args...)
	}

	rows, err := t.tx.QueryxContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	sliceVal := destVal.Elem()
	isPtr := sliceVal.Type().Elem().Kind() == reflect.Ptr
	baseType := sliceVal.Type().Elem()
	if isPtr {
		baseType = baseType.Elem()
	}

	for rows.Next() {
		results := make(map[string]interface{})
		if err := rows.MapScan(results); err != nil {
			return err
		}

		newItem := reflect.New(baseType)
		if err := scanMapToStruct(results, newItem.Interface()); err != nil {
			return err
		}

		if isPtr {
			sliceVal.Set(reflect.Append(sliceVal, newItem))
		} else {
			sliceVal.Set(reflect.Append(sliceVal, newItem.Elem()))
		}
	}

	return rows.Err()
}

func (t *sqlxTx) Get(ctx context.Context, dest any, query string, args ...any) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.Elem().Kind() != reflect.Struct {
		return t.tx.GetContext(ctx, dest, query, args...)
	}

	rows, err := t.tx.QueryxContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		return sql.ErrNoRows
	}

	results := make(map[string]interface{})
	if err := rows.MapScan(results); err != nil {
		return err
	}

	return scanMapToStruct(results, dest)
}

func (t *sqlxTx) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *sqlxTx) Begin(ctx context.Context) (Tx, error) {
	return nil, errors.New("cannot begin a transaction within a transaction")
}

func (t *sqlxTx) Commit() error {
	return t.tx.Commit()
}

func (t *sqlxTx) Rollback() error {
	return t.tx.Rollback()
}

type loggedTx struct {
	inner  Tx
	logger QueryLogger
}

func (t *loggedTx) Select(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := t.inner.Select(ctx, dest, query, args...)
	if t.logger != nil {
		t.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       err,
			Context:   ctxToMap(ctx),
		})
	}
	return err
}

func (t *loggedTx) Get(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := t.inner.Get(ctx, dest, query, args...)
	if t.logger != nil {
		logErr := err
		if errors.Is(err, sql.ErrNoRows) {
			logErr = nil
		}
		t.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       logErr,
			Context:   ctxToMap(ctx),
		})
	}
	return err
}

func (t *loggedTx) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	start := time.Now()
	res, err := t.inner.Exec(ctx, query, args...)
	var rows int64
	if err == nil && res != nil {
		rows, _ = res.RowsAffected()
	}
	if t.logger != nil {
		t.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       err,
			Rows:      rows,
			Context:   ctxToMap(ctx),
		})
	}
	return res, err
}

func (t *loggedTx) Begin(ctx context.Context) (Tx, error) {
	return nil, errors.New("cannot begin a transaction within a transaction")
}

func (t *loggedTx) Commit() error   { return t.inner.Commit() }
func (t *loggedTx) Rollback() error { return t.inner.Rollback() }

type LoggedExecutor struct {
	inner  Executor
	logger QueryLogger
}

func interpolateQuery(query string, args []any) string {
	if len(args) == 0 {
		return query
	}

	idx := 0
	result := reLogPlaceholder.ReplaceAllStringFunc(query, func(match string) string {
		if strings.HasPrefix(match, "'") {
			return match
		}

		if idx < len(args) {
			arg := args[idx]
			idx++
			return formatArg(arg)
		}
		return match
	})
	return result
}

func formatArg(arg any) string {
	switch v := arg.(type) {
	case string:
		return fmt.Sprintf("'%s'", strings.ReplaceAll(v, "'", "''"))
	case []byte:
		return fmt.Sprintf("'%x'", v)
	case time.Time:
		return fmt.Sprintf("'%s'", v.Format("2006-01-02 15:04:05"))
	default:
		return fmt.Sprintf("%v", arg)
	}
}

func (e *LoggedExecutor) Select(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := e.inner.Select(ctx, dest, query, args...)
	if e.logger != nil {
		e.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       err,
			Context:   ctxToMap(ctx),
		})
	}
	return err
}

func (e *LoggedExecutor) Get(ctx context.Context, dest any, query string, args ...any) error {
	start := time.Now()
	err := e.inner.Get(ctx, dest, query, args...)

	if e.logger != nil {
		logErr := err
		if errors.Is(err, sql.ErrNoRows) {
			logErr = nil
		}
		e.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       logErr,
			Context:   ctxToMap(ctx),
		})
	}
	return err
}

func (e *LoggedExecutor) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	start := time.Now()
	res, err := e.inner.Exec(ctx, query, args...)
	var rows int64
	if err == nil && res != nil {
		rows, _ = res.RowsAffected()
	}
	if e.logger != nil {
		e.logger.Log(QueryLog{
			Query:     query,
			FullQuery: interpolateQuery(query, args),
			Args:      args,
			Duration:  time.Since(start),
			Err:       err,
			Rows:      rows,
			Context:   ctxToMap(ctx),
		})
	}
	return res, err
}

func (e *LoggedExecutor) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &loggedTx{inner: tx, logger: e.logger}, nil
}

func ctxToMap(ctx context.Context) map[string]any {
	m := make(map[string]any)
	if reqID := ctx.Value(RequestIDKey); reqID != nil {
		m["request_id"] = reqID
	}
	return m
}

type dbCachedField struct {
	idx             int
	name            string
	isAutofill      bool
	isNullable      bool
	isPrimary       bool
	isAutoincrement bool
}

type dbCachedStruct struct {
	defaultName string
	fields      []dbCachedField
	primaryKeys []string
	autoIncCol  string
}

var dbStructCache sync.Map

func toSnakeCase(in string) string {
	runes := []rune(in)
	length := len(runes)

	var out []rune
	for i := 0; i < length; i++ {
		if i > 0 && unicode.IsUpper(runes[i]) && ((i+1 < length && unicode.IsLower(runes[i+1])) || unicode.IsLower(runes[i-1])) {
			out = append(out, '_')
		}
		out = append(out, unicode.ToLower(runes[i]))
	}
	return string(out)
}

func getStructInfo(typ reflect.Type) (*dbCachedStruct, error) {
	if cached, ok := dbStructCache.Load(typ); ok {
		return cached.(*dbCachedStruct), nil
	}

	info := &dbCachedStruct{}
	info.defaultName = toSnakeCase(typ.Name())

	ptrType := reflect.PointerTo(typ)
	if method, exists := ptrType.MethodByName("TableName"); exists {
		instance := reflect.New(typ)
		res := method.Func.Call([]reflect.Value{instance})
		if len(res) > 0 {
			if str, ok := res[0].Interface().(string); ok && str != "" {
				info.defaultName = str
			}
		}
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}

		switch field.Name {
		case "__db", "__res", "__self", "__hasSoftDelete":
			continue
		}

		tag := field.Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}

		parts := strings.Split(tag, ",")
		colName := parts[0]
		if colName == "" {
			continue
		}

		fInfo := dbCachedField{
			idx:  i,
			name: colName,
		}

		for _, opt := range parts[1:] {
			opt = strings.TrimSpace(opt)
			switch opt {
			case "autofill":
				fInfo.isAutofill = true
			case "nullable":
				fInfo.isNullable = true
			case "primary":
				fInfo.isPrimary = true
			case "autoincrement":
				fInfo.isAutoincrement = true
			}
		}

		if fInfo.isAutoincrement {
			fInfo.isPrimary = true
		}

		if fInfo.isPrimary {
			info.primaryKeys = append(info.primaryKeys, fInfo.name)
		}

		if fInfo.isAutoincrement {
			if info.autoIncCol == "" {
				info.autoIncCol = fInfo.name
			} else {
				LogWarn("[DB] Multiple autoincrement fields in struct %s, using first: %s", typ.Name(), info.autoIncCol)
			}
		}

		info.fields = append(info.fields, fInfo)
	}

	actual, _ := dbStructCache.LoadOrStore(typ, info)
	return actual.(*dbCachedStruct), nil
}

func getTableNameFromModel(model any) (string, error) {
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return "", errors.New("model is nil")
		}
		val = val.Elem()
	}

	typ := val.Type()

	if typ.Kind() == reflect.Slice {
		typ = typ.Elem()
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
	}

	if typ.Kind() != reflect.Struct {
		return "", errors.New("model must be a struct or slice of structs")
	}

	info, err := getStructInfo(typ)
	if err != nil {
		return "", err
	}

	return info.defaultName, nil
}

func isZero(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}

	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0.0
	case reflect.String:
		return v.String() == ""
	case reflect.Bool:
		return !v.Bool()
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Struct:
		if t, ok := v.Interface().(time.Time); ok {
			return t.IsZero()
		}
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	case reflect.Slice, reflect.Map:
		return v.IsNil() || v.Len() == 0
	}
	return false
}

func structToMap(model any, selectedCols []string, forcedNullable map[string]bool) (map[string]any, error) {
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil, errors.New("struct pointer is nil")
		}
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return nil, errors.New("input must be a struct")
	}

	typ := val.Type()
	info, err := getStructInfo(typ)
	if err != nil {
		return nil, err
	}

	colsSet := make(map[string]bool)
	if len(selectedCols) > 0 {
		for _, c := range selectedCols {
			colsSet[c] = true
		}
	}

	data := make(map[string]any)
	for _, f := range info.fields {
		if len(colsSet) > 0 {
			if !colsSet[f.name] {
				continue
			}
		} else {
			if f.isAutofill {
				continue
			}
			if f.isAutoincrement {
				continue
			}
		}

		fieldVal := val.Field(f.idx)
		valInterface := fieldVal.Interface()

		isNullable := f.isNullable || (forcedNullable != nil && forcedNullable[f.name])

		if isNullable {
			if isZero(fieldVal) {
				data[f.name] = nil
			} else {
				data[f.name] = valInterface
			}
		} else {
			data[f.name] = valInterface
		}
	}
	return data, nil
}

func assignAutoIncrementID(model any, id int64) {
	if model == nil || id == 0 {
		return
	}

	val := reflect.ValueOf(model)
	if val.Kind() != reflect.Ptr {
		return
	}
	if val.IsNil() {
		return
	}
	val = val.Elem()

	if val.Kind() != reflect.Struct {
		return
	}

	typ := val.Type()
	info, err := getStructInfo(typ)
	if err != nil {
		return
	}

	for _, f := range info.fields {
		if f.isAutoincrement {
			fieldVal := val.Field(f.idx)
			if fieldVal.CanSet() {
				switch fieldVal.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					fieldVal.SetInt(id)
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					fieldVal.SetUint(uint64(id))
				}
			}
			return
		}
	}

	for _, f := range info.fields {
		if f.name == "id" {
			fieldVal := val.Field(f.idx)
			if fieldVal.CanSet() {
				switch fieldVal.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					fieldVal.SetInt(id)
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					fieldVal.SetUint(uint64(id))
				}
			}
			return
		}
	}
}

type whereClause struct {
	boolean string
	expr    string
	args    []any
}

type joinClause struct {
	typ   string
	table string
	on    string
}

type selectClause struct {
	expr string
	args []any
}

type Builder struct {
	table    string
	selects  []selectClause
	joins    []joinClause
	wheres   []whereClause
	groupBys []string
	havings  []whereClause
	orderBys []string
	limit    *int
	offset   *int
	args     []any

	action  string
	inserts map[string]any
	updates map[string]any

	nullableCols map[string]bool

	onDuplicateUpdateCols []string

	primaryKeys []string
	autoIncCol  string

	rawQuery  string
	rawArgs   []any
	namedArgs map[string]any

	dialect  Dialect
	executor Executor
	ctx      context.Context

	dest             any
	res              *Resources
	defaultCondition DefaultConditionFunc

	softDeleteCondition string
}

func sanitizeIdentifier(ident string) string {
	if ident == "" {
		return ""
	}
	if reValidIdentifier.MatchString(ident) {
		return ident
	}
	LogError("[DB Security] Potential SQL Injection blocked in identifier: %s", ident)
	return ""
}

func sanitizeOrderBy(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	lowerExpr := strings.ToLower(expr)
	if strings.Contains(lowerExpr, "select") || strings.Contains(lowerExpr, "case") || strings.Contains(lowerExpr, "when") {
		LogError("[DB Security] Suspicious keywords in OrderBy blocked: %s", expr)
		return ""
	}

	if reValidOrderBy.MatchString(expr) {
		return expr
	}
	LogError("[DB Security] Invalid OrderBy expression blocked: %s", expr)
	return ""
}

func sanitizeOnClause(on string) string {
	onLower := strings.ToLower(on)
	if strings.Contains(on, "--") || strings.Contains(on, "/*") {
		LogError("[DB Security] Suspicious ON clause blocked (comment): %s", on)
		return "1=0"
	}
	if strings.Contains(on, ";") {
		LogError("[DB Security] Suspicious ON clause blocked (semicolon): %s", on)
		return "1=0"
	}
	if strings.Contains(on, "(") || strings.Contains(on, ")") {
		LogError("[DB Security] Suspicious ON clause blocked (parentheses): %s", on)
		return "1=0"
	}
	if strings.Contains(onLower, "select ") || strings.Contains(onLower, " union ") {
		LogError("[DB Security] Suspicious ON clause blocked (keyword): %s", on)
		return "1=0"
	}

	return on
}

func (b *Builder) Bind(dest any) *Builder {
	b.dest = dest
	return b
}

func (b *Builder) SetResources(res *Resources) *Builder {
	b.res = res
	return b
}

func (b *Builder) WithContext(ctx context.Context) *Builder {
	b.ctx = ctx
	return b
}

func (b *Builder) Use(tx Tx) *Builder {
	b.executor = tx
	return b
}

func (b *Builder) Cols(cols ...string) *Builder {
	for _, c := range cols {
		b.selects = append(b.selects, selectClause{expr: c})
	}
	return b
}

func (b *Builder) Nullable(cols ...string) *Builder {
	if b.nullableCols == nil {
		b.nullableCols = make(map[string]bool)
	}
	for _, c := range cols {
		b.nullableCols[c] = true
	}
	return b
}

func (b *Builder) Upsert(data any, updateCols ...string) (Result, error) {
	b.onDuplicateUpdateCols = updateCols
	return b.Insert(data)
}

func (b *Builder) WhereEq(col string, val any) *Builder {
	safeCol := sanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s = ?", safeCol), val)
}

func (b *Builder) Where(expr string, args ...any) *Builder {
	b.wheres = append(b.wheres, whereClause{boolean: "AND", expr: expr, args: args})
	return b
}

func (b *Builder) OrWhere(expr string, args ...any) *Builder {
	b.wheres = append(b.wheres, whereClause{boolean: "OR", expr: expr, args: args})
	return b
}

func (b *Builder) WhereIn(col string, vals []any) *Builder {
	safeCol := sanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}

	if len(vals) == 0 {
		return b.Where("1 = 0")
	}

	placeholders := make([]string, len(vals))
	for i := range vals {
		placeholders[i] = "?"
	}
	expr := fmt.Sprintf("%s IN (%s)", safeCol, strings.Join(placeholders, ", "))
	return b.Where(expr, vals...)
}

func (b *Builder) WhereNull(col string) *Builder {
	safeCol := sanitizeIdentifier(col)
	if safeCol == "" {
		return b
	}
	return b.Where(fmt.Sprintf("%s IS NULL", safeCol))
}

func (b *Builder) Join(table, on string, typ string) *Builder {
	safeTable := sanitizeIdentifier(table)
	if safeTable == "" {
		return b
	}

	safeOn := sanitizeOnClause(on)

	b.joins = append(b.joins, joinClause{typ: typ, table: safeTable, on: safeOn})
	return b
}

func (b *Builder) LeftJoin(table, on string) *Builder {
	return b.Join(table, on, "LEFT")
}

func (b *Builder) RightJoin(table, on string) *Builder {
	return b.Join(table, on, "RIGHT")
}

func (b *Builder) InnerJoin(table, on string) *Builder {
	return b.Join(table, on, "INNER")
}

func (b *Builder) GroupBy(cols ...string) *Builder {
	for i, c := range cols {
		cols[i] = sanitizeIdentifier(c)
	}
	validCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if c != "" {
			validCols = append(validCols, c)
		}
	}
	b.groupBys = append(b.groupBys, validCols...)
	return b
}

func (b *Builder) Having(expr string, args ...any) *Builder {
	b.havings = append(b.havings, whereClause{boolean: "AND", expr: expr, args: args})
	return b
}

func (b *Builder) OrderBy(expr string) *Builder {
	safeExpr := sanitizeOrderBy(expr)
	if safeExpr != "" {
		b.orderBys = append(b.orderBys, safeExpr)
	}
	return b
}

func (b *Builder) Limit(n int) *Builder {
	b.limit = &n
	return b
}

func (b *Builder) Offset(n int) *Builder {
	b.offset = &n
	return b
}

func (b *Builder) Clone() *Builder {
	newB := *b

	if b.selects != nil {
		newB.selects = make([]selectClause, len(b.selects))
		for i, s := range b.selects {
			newB.selects[i].expr = s.expr
			if len(s.args) > 0 {
				newB.selects[i].args = make([]any, len(s.args))
				copy(newB.selects[i].args, s.args)
			}
		}
	}
	if b.wheres != nil {
		newB.wheres = make([]whereClause, len(b.wheres))
		for i, w := range b.wheres {
			newB.wheres[i].boolean = w.boolean
			newB.wheres[i].expr = w.expr
			if len(w.args) > 0 {
				newB.wheres[i].args = make([]any, len(w.args))
				copy(newB.wheres[i].args, w.args)
			}
		}
	}
	if b.joins != nil {
		newB.joins = make([]joinClause, len(b.joins))
		copy(newB.joins, b.joins)
	}
	if b.groupBys != nil {
		newB.groupBys = make([]string, len(b.groupBys))
		copy(newB.groupBys, b.groupBys)
	}
	if b.havings != nil {
		newB.havings = make([]whereClause, len(b.havings))
		for i, h := range b.havings {
			newB.havings[i].boolean = h.boolean
			newB.havings[i].expr = h.expr
			if len(h.args) > 0 {
				newB.havings[i].args = make([]any, len(h.args))
				copy(newB.havings[i].args, h.args)
			}
		}
	}
	if b.orderBys != nil {
		newB.orderBys = make([]string, len(b.orderBys))
		copy(newB.orderBys, b.orderBys)
	}
	if b.args != nil {
		newB.args = make([]any, len(b.args))
		copy(newB.args, b.args)
	}
	if b.nullableCols != nil {
		newB.nullableCols = make(map[string]bool)
		for k, v := range b.nullableCols {
			newB.nullableCols[k] = v
		}
	}
	if b.onDuplicateUpdateCols != nil {
		newB.onDuplicateUpdateCols = make([]string, len(b.onDuplicateUpdateCols))
		copy(newB.onDuplicateUpdateCols, b.onDuplicateUpdateCols)
	}
	if b.primaryKeys != nil {
		newB.primaryKeys = make([]string, len(b.primaryKeys))
		copy(newB.primaryKeys, b.primaryKeys)
	}
	if b.inserts != nil {
		newB.inserts = make(map[string]any, len(b.inserts))
		for k, v := range b.inserts {
			newB.inserts[k] = v
		}
	}
	if b.updates != nil {
		newB.updates = make(map[string]any, len(b.updates))
		for k, v := range b.updates {
			newB.updates[k] = v
		}
	}
	if b.rawArgs != nil {
		newB.rawArgs = make([]any, len(b.rawArgs))
		copy(newB.rawArgs, b.rawArgs)
	}
	if b.namedArgs != nil {
		newB.namedArgs = make(map[string]any, len(b.namedArgs))
		for k, v := range b.namedArgs {
			newB.namedArgs[k] = v
		}
	}

	if b.dest != nil {
		newB.dest = b.dest
	}

	return &newB
}

func (b *Builder) applyDefaultCondition() {
	if len(b.wheres) > 0 {
		return
	}

	if b.defaultCondition == nil || b.res == nil {
		return
	}

	var fields []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			fields = append(fields, s.expr)
		}
	} else if b.dest != nil {
		val := reflect.ValueOf(b.dest)
		if val.Kind() == reflect.Ptr {
			if val.IsNil() {
				return
			}
			val = val.Elem()
		}
		if val.Kind() == reflect.Struct {
			typ := val.Type()
			info, err := getStructInfo(typ)
			if err == nil {
				for _, f := range info.fields {
					fields = append(fields, f.name)
				}
			}
		}
	}

	conds := b.defaultCondition(fields, b.res)
	if len(conds) > 0 {
		keys := make([]string, 0, len(conds))
		for k := range conds {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := conds[k]
			b.WhereEq(k, v)
		}
	}
}

func (b *Builder) Insert(data ...any) (Result, error) {
	var d any
	if len(data) > 0 {
		d = data[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return nil, errors.New("insert data cannot be empty")
	}

	var tableName string
	var mapData map[string]any
	var structInfo *dbCachedStruct

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	val := reflect.ValueOf(d)
	if val.Kind() == reflect.Map {
		var m map[string]any
		switch t := d.(type) {
		case map[string]any:
			m = t
		default:
			return nil, errors.New("unsupported map type")
		}

		mapData = make(map[string]any)
		filterSet := make(map[string]bool)
		if len(selectedCols) > 0 {
			for _, c := range selectedCols {
				filterSet[c] = true
			}
		}

		for k, v := range m {
			if sanitizeIdentifier(k) == "" {
				continue
			}

			if len(filterSet) > 0 && !filterSet[k] {
				continue
			}

			if b.nullableCols != nil && b.nullableCols[k] {
				rv := reflect.ValueOf(v)
				if !rv.IsValid() || isZero(rv) {
					mapData[k] = nil
				} else {
					mapData[k] = v
				}
			} else {
				mapData[k] = v
			}
		}
	} else {
		var err error
		tableName, err = getTableNameFromModel(d)
		if err != nil {
			return nil, err
		}
		mapData, err = structToMap(d, selectedCols, b.nullableCols)
		if err != nil {
			return nil, err
		}

		v := reflect.ValueOf(d)
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return nil, errors.New("struct pointer is nil")
			}
			v = v.Elem()
		}
		if v.Kind() == reflect.Struct {
			info, err := getStructInfo(v.Type())
			if err == nil {
				structInfo = info
			}
		}
	}

	if len(mapData) == 0 {
		return nil, errors.New("insert data cannot be empty")
	}

	clone := b.Clone()
	clone.action = "insert"
	clone.inserts = mapData

	if structInfo != nil {
		if len(structInfo.primaryKeys) > 0 {
			clone.primaryKeys = structInfo.primaryKeys
		}
		if structInfo.autoIncCol != "" {
			clone.autoIncCol = structInfo.autoIncCol
		}
	}

	if clone.table == "" {
		if tableName == "" {
			return nil, errors.New("table name is required")
		}
		clone.table = sanitizeIdentifier(tableName)
		if clone.table == "" {
			return nil, errors.New("invalid table name inferred from model")
		}
	}

	query, args := clone.Build()

	if clone.autoIncCol != "" && clone.dialect.Name() == "postgres" {
		var id int64
		err := clone.executor.Get(clone.ctx, &id, query, args...)
		if err != nil {
			return nil, err
		}
		assignAutoIncrementID(d, id)
		return &insertResult{lastInsertId: id, rowsAffected: 1}, nil
	}

	res, err := clone.executor.Exec(clone.ctx, query, args...)

	if err == nil && res != nil {
		if id, idErr := res.LastInsertId(); idErr == nil {
			assignAutoIncrementID(d, id)
		}
	}

	return res, err
}

func (b *Builder) execUpdate(data any, skipWhereCheck bool) (Result, error) {
	b.applyDefaultCondition()

	if !skipWhereCheck && len(b.wheres) == 0 {
		return nil, errors.New("update requires where clause (safety check)")
	}

	var tableName string
	var mapData map[string]any

	var selectedCols []string
	if len(b.selects) > 0 {
		for _, s := range b.selects {
			selectedCols = append(selectedCols, s.expr)
		}
	}

	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Map {
		var m map[string]any
		switch t := data.(type) {
		case map[string]any:
			m = t
		default:
			return nil, errors.New("unsupported map type")
		}

		mapData = make(map[string]any)
		filterSet := make(map[string]bool)
		if len(selectedCols) > 0 {
			for _, c := range selectedCols {
				filterSet[c] = true
			}
		}

		for k, v := range m {
			if sanitizeIdentifier(k) == "" {
				continue
			}

			if len(filterSet) > 0 && !filterSet[k] {
				continue
			}
			if b.nullableCols != nil && b.nullableCols[k] {
				rv := reflect.ValueOf(v)
				if !rv.IsValid() || isZero(rv) {
					mapData[k] = nil
				} else {
					mapData[k] = v
				}
			} else {
				mapData[k] = v
			}
		}
	} else {
		var err error
		tableName, err = getTableNameFromModel(data)
		if err != nil {
			return nil, err
		}
		mapData, err = structToMap(data, selectedCols, b.nullableCols)
		if err != nil {
			return nil, err
		}
	}

	if len(mapData) == 0 {
		return nil, errors.New("update data cannot be empty")
	}

	clone := b.Clone()
	clone.action = "update"
	clone.updates = mapData

	if clone.table == "" {
		if tableName == "" {
			return nil, errors.New("table name is required")
		}
		clone.table = sanitizeIdentifier(tableName)
		if clone.table == "" {
			return nil, errors.New("invalid table name inferred from model")
		}
	}

	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)

	return res, err
}

func (b *Builder) Update(data ...any) (Result, error) {
	var d any
	if len(data) > 0 {
		d = data[0]
	} else {
		d = b.dest
	}
	if d == nil {
		return nil, errors.New("update data cannot be empty")
	}
	return b.execUpdate(d, false)
}

func (b *Builder) execDelete(model any, skipWhereCheck bool) (Result, error) {
	b.applyDefaultCondition()

	if b.softDeleteCondition != "" {
		return b.execUpdate(map[string]any{"deleted_at": time.Now()}, skipWhereCheck)
	}

	if !skipWhereCheck && len(b.wheres) == 0 {
		return nil, errors.New("delete requires where clause (safety check)")
	}

	clone := b.Clone()
	clone.action = "delete"

	if model != nil {
		tblName, err := getTableNameFromModel(model)
		if err != nil {
			return nil, err
		}

		if clone.table == "" {
			clone.table = sanitizeIdentifier(tblName)
			if clone.table == "" {
				return nil, errors.New("invalid table name inferred from model")
			}
		}
	}

	if clone.table == "" {
		return nil, errors.New("table name is required")
	}

	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)
	return res, err
}

func (b *Builder) Delete(models ...any) (Result, error) {
	var d any
	if len(models) > 0 {
		d = models[0]
	} else {
		d = b.dest
	}

	return b.execDelete(d, false)
}

func (b *Builder) Purge() (Result, error) {
	b.softDeleteCondition = ""
	return b.execDelete(nil, false)
}

func (b *Builder) Find(dest ...any) (bool, error) {
	b.applyDefaultCondition()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return false, errors.New("Find: destination is nil")
	}

	clone := b.Clone()
	clone.Limit(1)

	val := reflect.ValueOf(d)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return false, errors.New("model is nil")
		}
		val = val.Elem()
	}
	typ := val.Type()

	if typ.Kind() == reflect.Slice {
		typ = typ.Elem()
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
	}

	if typ.Kind() == reflect.Struct {
		info, err := getStructInfo(typ)
		if err != nil {
			return false, fmt.Errorf("Find: %v", err)
		}

		if len(clone.selects) == 0 {
			for _, f := range info.fields {
				clone.Cols(f.name)
			}
		}

		if clone.table == "" {
			clone.table = sanitizeIdentifier(info.defaultName)
		}
	} else {
		if clone.table == "" {
			tblName, err := getTableNameFromModel(d)
			if err != nil {
				return false, fmt.Errorf("Find: %v", err)
			}
			clone.table = sanitizeIdentifier(tblName)
		}
	}

	if clone.table == "" {
		return false, errors.New("Find: table name is required but empty")
	}

	query, args := clone.Build()
	err := clone.executor.Get(clone.ctx, d, query, args...)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b *Builder) FindAll(dest ...any) error {
	b.applyDefaultCondition()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return errors.New("FindAll: destination is nil")
	}

	clone := b.Clone()

	sliceVal := reflect.ValueOf(d)
	if sliceVal.Kind() != reflect.Ptr || sliceVal.Elem().Kind() != reflect.Slice {
		if clone.table == "" {
			tblName, err := getTableNameFromModel(d)
			if err != nil {
				return fmt.Errorf("FindAll: %v", err)
			}
			clone.table = sanitizeIdentifier(tblName)
		}
	} else {
		typ := sliceVal.Elem().Type().Elem()
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}

		if typ.Kind() == reflect.Struct {
			info, err := getStructInfo(typ)
			if err != nil {
				return fmt.Errorf("FindAll: %v", err)
			}

			if len(clone.selects) == 0 {
				for _, f := range info.fields {
					clone.Cols(f.name)
				}
			}

			if clone.table == "" {
				clone.table = sanitizeIdentifier(info.defaultName)
			}
		}
	}

	if clone.table == "" {
		return errors.New("FindAll: table name is required")
	}

	query, args := clone.Build()
	return clone.executor.Select(clone.ctx, d, query, args...)
}

func (b *Builder) Count() (int64, error) {
	b.applyDefaultCondition()

	clone := b.Clone()
	clone.selects = nil
	clone.Cols("COUNT(*)")

	if clone.table == "" {
		return 0, errors.New("Count: table name is required")
	}

	query, args := clone.Build()
	var count int64
	err := clone.executor.Get(clone.ctx, &count, query, args...)
	return count, err
}

func (b *Builder) Exist() (bool, error) {
	b.applyDefaultCondition()

	clone := b.Clone()
	clone.selects = nil
	clone.Cols("1")
	clone.Limit(1)

	if clone.table == "" {
		return false, errors.New("Exist: table name is required")
	}

	query, args := clone.Build()
	var exists int
	err := clone.executor.Get(clone.ctx, &exists, query, args...)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *Builder) Exec() (Result, error) {
	clone := b.Clone()
	query, args := clone.Build()
	res, err := clone.executor.Exec(clone.ctx, query, args...)
	return res, err
}

func (b *Builder) Debug() (string, []any) {
	return b.Build()
}

func (b *Builder) DebugSQL() string {
	q, args := b.Build()
	return interpolateQuery(q, args)
}

func (b *Builder) ToSQL() string {
	q, _ := b.Build()
	return q
}

func (b *Builder) Build() (string, []any) {
	raw, args := b.buildRaw()
	return b.dialect.ConvertPlaceholders(raw), args
}

func (b *Builder) buildRaw() (string, []any) {
	if b.namedArgs != nil && b.rawQuery != "" {
		query, args, err := sqlx.Named(b.rawQuery, b.namedArgs)
		if err != nil {
			LogError("[DB] Named query error: %v", err)
			return b.rawQuery, nil
		}
		return query, args
	}

	if b.rawQuery != "" {
		return b.rawQuery, b.rawArgs
	}

	b.args = nil
	var sql strings.Builder

	switch b.action {
	case "insert":
		b.buildInsert(&sql)
	case "update":
		b.buildUpdate(&sql)
	case "delete":
		b.buildDelete(&sql)
	default:
		b.buildSelect(&sql)
	}

	return sql.String(), b.args
}

func (b *Builder) buildSelect(sql *strings.Builder) {
	sql.WriteString("SELECT ")
	if len(b.selects) > 0 {
		for i, s := range b.selects {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(s.expr)
			if len(s.args) > 0 {
				b.args = append(b.args, s.args...)
			}
		}
	} else {
		sql.WriteString("*")
	}

	if b.table == "" {
		sql.WriteString(" FROM undefined_table ")
		LogError("[DB] Build select without table name")
	} else {
		sql.WriteString(" FROM " + b.table)
	}

	for _, j := range b.joins {
		sql.WriteString(fmt.Sprintf(" %s JOIN %s ON %s", j.typ, j.table, j.on))
	}

	b.buildWheres(sql)

	if len(b.groupBys) > 0 {
		sql.WriteString(" GROUP BY " + strings.Join(b.groupBys, ", "))
	}

	if len(b.havings) > 0 {
		sql.WriteString(" HAVING ")
		for i, h := range b.havings {
			if i > 0 {
				sql.WriteString(" " + h.boolean + " ")
			}
			sql.WriteString(h.expr)
			b.args = append(b.args, h.args...)
		}
	}

	if len(b.orderBys) > 0 {
		sql.WriteString(" ORDER BY " + strings.Join(b.orderBys, ", "))
	}

	if b.limit != nil {
		sql.WriteString(" LIMIT ?")
		b.args = append(b.args, *b.limit)
	}

	if b.offset != nil {
		sql.WriteString(" OFFSET ?")
		b.args = append(b.args, *b.offset)
	}
}

func (b *Builder) buildInsert(sql *strings.Builder) {
	var cols []string
	var placeholders []string

	keys := make([]string, 0, len(b.inserts))
	for k := range b.inserts {
		if sanitizeIdentifier(k) == "" {
			LogError("[DB Security] Invalid column name in insert map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := sanitizeIdentifier(k)
		cols = append(cols, safeCol)
		placeholders = append(placeholders, "?")
		b.args = append(b.args, b.inserts[k])
	}

	if len(cols) == 0 {
		LogError("[DB] Build insert with no valid columns")
		return
	}

	sql.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", b.table, strings.Join(cols, ", "), strings.Join(placeholders, ", ")))

	if len(b.onDuplicateUpdateCols) > 0 {
		if b.dialect.Name() == "mysql" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := sanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = VALUES(%s)", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				sql.WriteString(" ON DUPLICATE KEY UPDATE ")
				sql.WriteString(strings.Join(updates, ", "))
			}
		} else if b.dialect.Name() == "postgres" {
			var updates []string
			for _, col := range b.onDuplicateUpdateCols {
				safeCol := sanitizeIdentifier(col)
				if safeCol != "" {
					updates = append(updates, fmt.Sprintf("%s = EXCLUDED.%s", safeCol, safeCol))
				}
			}
			if len(updates) > 0 {
				conflictCols := "id"
				if len(b.primaryKeys) > 0 {
					conflictCols = strings.Join(b.primaryKeys, ", ")
				}
				sql.WriteString(fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET ", conflictCols))
				sql.WriteString(strings.Join(updates, ", "))
			}
		} else {
			LogWarn("[DB] Upsert ON DUPLICATE KEY UPDATE / ON CONFLICT is only supported for MySQL and PostgreSQL dialects.")
		}
	}

	if b.autoIncCol != "" && b.dialect.Name() == "postgres" {
		sql.WriteString(fmt.Sprintf(" RETURNING %s", sanitizeIdentifier(b.autoIncCol)))
	}
}

func (b *Builder) buildUpdate(sql *strings.Builder) {
	var sets []string

	keys := make([]string, 0, len(b.updates))
	for k := range b.updates {
		if sanitizeIdentifier(k) == "" {
			LogError("[DB Security] Invalid column name in update map: %s", k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		safeCol := sanitizeIdentifier(k)
		sets = append(sets, fmt.Sprintf("%s = ?", safeCol))
		b.args = append(b.args, b.updates[k])
	}

	if len(sets) == 0 {
		LogError("[DB] Build update with no valid columns")
		return
	}

	sql.WriteString(fmt.Sprintf("UPDATE %s SET %s", b.table, strings.Join(sets, ", ")))
	b.buildWheres(sql)
}

func (b *Builder) buildDelete(sql *strings.Builder) {
	sql.WriteString("DELETE FROM " + b.table)
	b.buildWheres(sql)
}

func (b *Builder) buildWheres(sql *strings.Builder) {
	if len(b.wheres) == 0 && b.softDeleteCondition == "" {
		return
	}

	sql.WriteString(" WHERE ")

	if b.softDeleteCondition != "" {
		sql.WriteString(b.softDeleteCondition)

		if len(b.wheres) > 0 {
			hasOr := false
			for _, w := range b.wheres {
				if w.boolean == "OR" {
					hasOr = true
					break
				}
			}

			if hasOr {
				sql.WriteString(" AND (")
				for i, w := range b.wheres {
					if i > 0 {
						sql.WriteString(" " + w.boolean + " ")
					}
					sql.WriteString(w.expr)
					b.args = append(b.args, w.args...)
				}
				sql.WriteString(")")
			} else {
				for _, w := range b.wheres {
					sql.WriteString(" AND ")
					sql.WriteString(w.expr)
					b.args = append(b.args, w.args...)
				}
			}
		}
	} else {
		for i, w := range b.wheres {
			if i > 0 {
				sql.WriteString(" " + w.boolean + " ")
			}
			sql.WriteString(w.expr)
			b.args = append(b.args, w.args...)
		}
	}
}
