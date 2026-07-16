package kiya

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/fuadarradhi/kiya/internal/db"
)

var ErrRecordNotFound = db.ErrRecordNotFound

func notFoundErrPlaceholder() error {
	return NewHTTPError(404, "data tidak ditemukan", ErrRecordNotFound)
}

type BaseModel struct {
	__db            *DB
	__ctx           *Context
	__self          any
	__hasSoftDelete bool
	__tx            Tx
}

func (b *BaseModel) Init(database *DB, ctx *Context, self any) {
	b.__db = database
	b.__ctx = ctx
	b.__self = self

	val := reflect.ValueOf(self)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return
		}
		val = val.Elem()
	}

	if val.Kind() == reflect.Struct {
		field := val.FieldByName("DeletedAt")
		if field.IsValid() {
			b.__hasSoftDelete = true
		}
	}
}

func (b *BaseModel) SetTx(tx Tx)   { b.__tx = tx }
func (b *BaseModel) DB() *DB       { return b.__db }
func (b *BaseModel) Res() *Context { return b.__ctx }

func (b *BaseModel) TableName() string {
	if b.__self == nil {
		return ""
	}
	name, _ := db.GetTableNameFromModel(b.__self)
	return name
}

func (b *BaseModel) newBuilder() *Builder {
	if b.__db == nil || b.__self == nil {
		panic("kiya: model is not initialized properly")
	}

	var dbConn *DB
	if b.__tx != nil {
		dbConn = b.__db.Use(b.__tx)
	} else {
		dbConn = b.__db
	}

	builder := dbConn.Table(b.TableName()).Bind(b.__self).SetResources(b.__ctx)

	if b.__hasSoftDelete {
		builder.SetSoftDeleteCondition("deleted_at IS NULL")
	}

	return builder
}

func (b *BaseModel) resolveIDFromURL() (int64, error) {
	if b.__ctx == nil {
		return 0, errors.New("kiya: no request context available to resolve id from url")
	}

	rawID := b.__ctx.Get("id")
	if rawID == "" {
		return 0, errors.New("kiya: id is required in url for this operation")
	}

	id, err := b.__ctx.DecryptID(rawID)
	if err != nil {
		return 0, fmt.Errorf("kiya: invalid id in url: %w", err)
	}
	return id, nil
}

func (b *BaseModel) Validate() error {
	if b.__ctx == nil || b.__self == nil {
		return errors.New("model is not initialized properly")
	}
	v := b.__ctx.Validator(b.__self, true)
	return v.Validate()
}

func (b *BaseModel) Cols(cols ...string) *Builder     { return b.newBuilder().Cols(cols...) }
func (b *BaseModel) Nullable(cols ...string) *Builder { return b.newBuilder().Nullable(cols...) }
func (b *BaseModel) NoScope() *Builder                { return b.newBuilder().NoScope() }
func (b *BaseModel) Unsafe() *Builder                 { return b.newBuilder().Unsafe() }
func (b *BaseModel) NoHistory() *Builder              { return b.newBuilder().NoHistory() }
func (b *BaseModel) Where(expr string, args ...any) *Builder {
	return b.newBuilder().Where(expr, args...)
}
func (b *BaseModel) OrWhere(expr string, args ...any) *Builder {
	return b.newBuilder().OrWhere(expr, args...)
}
func (b *BaseModel) WhereEq(col string, val any) *Builder { return b.newBuilder().WhereEq(col, val) }
func (b *BaseModel) WhereNotEq(col string, val any) *Builder {
	return b.newBuilder().WhereNotEq(col, val)
}
func (b *BaseModel) WhereGt(col string, val any) *Builder  { return b.newBuilder().WhereGt(col, val) }
func (b *BaseModel) WhereGte(col string, val any) *Builder { return b.newBuilder().WhereGte(col, val) }
func (b *BaseModel) WhereLt(col string, val any) *Builder  { return b.newBuilder().WhereLt(col, val) }
func (b *BaseModel) WhereLte(col string, val any) *Builder { return b.newBuilder().WhereLte(col, val) }
func (b *BaseModel) WhereLike(col string, pattern string) *Builder {
	return b.newBuilder().WhereLike(col, pattern)
}
func (b *BaseModel) WhereIn(col string, vals []any) *Builder {
	return b.newBuilder().WhereIn(col, vals)
}
func (b *BaseModel) WhereNotIn(col string, vals []any) *Builder {
	return b.newBuilder().WhereNotIn(col, vals)
}
func (b *BaseModel) WhereNull(col string) *Builder { return b.newBuilder().WhereNull(col) }
func (b *BaseModel) Join(table, on string, typ string) *Builder {
	return b.newBuilder().Join(table, on, typ)
}
func (b *BaseModel) LeftJoin(table, on string) *Builder  { return b.newBuilder().LeftJoin(table, on) }
func (b *BaseModel) RightJoin(table, on string) *Builder { return b.newBuilder().RightJoin(table, on) }
func (b *BaseModel) InnerJoin(table, on string) *Builder { return b.newBuilder().InnerJoin(table, on) }
func (b *BaseModel) GroupBy(cols ...string) *Builder     { return b.newBuilder().GroupBy(cols...) }
func (b *BaseModel) Having(expr string, args ...any) *Builder {
	return b.newBuilder().Having(expr, args...)
}
func (b *BaseModel) OrderBy(expr string) *Builder             { return b.newBuilder().OrderBy(expr) }
func (b *BaseModel) Limit(n int) *Builder                     { return b.newBuilder().Limit(n) }
func (b *BaseModel) Offset(n int) *Builder                    { return b.newBuilder().Offset(n) }
func (b *BaseModel) WithContext(ctx context.Context) *Builder { return b.newBuilder().WithContext(ctx) }
func (b *BaseModel) Use(tx Tx) *Builder                       { return b.newBuilder().Use(tx) }

func (b *BaseModel) Insert() error {
	return b.newBuilder().Insert()
}

func (b *BaseModel) Find() (bool, error) {
	if b.__ctx == nil {
		return false, nil
	}
	rawID := b.__ctx.Get("id")
	if rawID == "" {
		return false, nil
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return false, err
	}
	id, err := b.__ctx.DecryptID(rawID)
	if err != nil {
		return false, fmt.Errorf("kiya: invalid id in url: %w", err)
	}
	return b.newBuilder().WhereEq(pkCol, id).Find()
}

func (b *BaseModel) FindOrFail() error {
	if b.__ctx == nil {
		return notFoundErrPlaceholder()
	}
	rawID := b.__ctx.Get("id")
	if rawID == "" {
		return notFoundErrPlaceholder()
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return err
	}
	id, err := b.__ctx.DecryptID(rawID)
	if err != nil {
		return fmt.Errorf("kiya: invalid id in url: %w", err)
	}
	return b.newBuilder().WhereEq(pkCol, id).FindOrFail()
}

func (b *BaseModel) FindAll() error {
	return b.newBuilder().FindAll()
}

func (b *BaseModel) UpdateAll() (int64, error) {
	return b.newBuilder().UpdateAll(b.__self)
}

func (b *BaseModel) Upsert(cols ...string) error {
	if b.__ctx == nil {
		return errors.New("kiya: Upsert requires request context to resolve id from url")
	}

	builder := b.newBuilder().Cols(cols...)

	rawID := b.__ctx.Get("id")
	if rawID != "" {
		pkCol, err := db.PrimaryKeyColumn(b.__self)
		if err != nil {
			return err
		}
		id, err := b.__ctx.DecryptID(rawID)
		if err != nil {
			return fmt.Errorf("kiya: invalid id in url: %w", err)
		}
		builder.WhereEq(pkCol, id)
	}

	return builder.Upsert()
}

func (b *BaseModel) Update(cols ...string) error {
	id, err := b.resolveIDFromURL()
	if err != nil {
		return err
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return err
	}
	return b.newBuilder().Cols(cols...).WhereEq(pkCol, id).Update()
}

func (b *BaseModel) Delete() error {
	id, err := b.resolveIDFromURL()
	if err != nil {
		return err
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return err
	}
	return b.newBuilder().WhereEq(pkCol, id).Delete()
}

func (b *BaseModel) Restore() error {
	id, err := b.resolveIDFromURL()
	if err != nil {
		return err
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return err
	}
	return b.newBuilder().WhereEq(pkCol, id).Restore()
}

func (b *BaseModel) Purge() error {
	id, err := b.resolveIDFromURL()
	if err != nil {
		return err
	}
	pkCol, err := db.PrimaryKeyColumn(b.__self)
	if err != nil {
		return err
	}
	return b.newBuilder().WhereEq(pkCol, id).Purge()
}

func (b *BaseModel) PurgeAll() (int64, error) {
	return b.newBuilder().PurgeAll(b.__self)
}

func (b *BaseModel) Retain(keepIDs []int64, purge bool, where ...WhereFunc) (int64, error) {
	return b.newBuilder().Retain(keepIDs, purge, where...)
}

func (b *BaseModel) Count() (int64, error) { return b.newBuilder().Count() }
func (b *BaseModel) Exist() (bool, error)  { return b.newBuilder().Exist() }

func (b *BaseModel) Paginate(page, perPage int) (*db.Pagination, error) {
	return b.newBuilder().Paginate(b.__self, page, perPage)
}

func (b *BaseModel) WithDeleted() *Builder {
	builder := b.newBuilder()
	builder.ClearSoftDeleteCondition()
	return builder
}

func (b *BaseModel) Raw(query string, args ...any) *Builder {
	if b.__db == nil {
		panic("kiya: model is not initialized, database is nil")
	}
	return b.__db.Raw(query, args...).SetResources(b.__ctx)
}

func (b *BaseModel) Table(name string) *Builder {
	if b.__db == nil {
		panic("kiya: model is not initialized, database is nil")
	}
	return b.__db.Table(name).SetResources(b.__ctx)
}
