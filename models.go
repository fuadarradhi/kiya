package kiya

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/fuadarradhi/kiya/internal/db"
)

type BaseModel struct {
	__db            *DB
	__res           *Resources
	__self          any
	__hasSoftDelete bool
}

func (b *BaseModel) Init(database *DB, res *Resources, self any) {
	b.__db = database
	b.__res = res
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

func (b *BaseModel) DB() *DB {
	return b.__db
}

func (b *BaseModel) Res() *Resources {
	return b.__res
}

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
	builder := b.__db.Table(b.TableName()).Bind(b.__self).SetResources(b.__res)

	if b.__hasSoftDelete {
		builder.SetSoftDeleteCondition("deleted_at IS NULL")
	}

	return builder
}

func (b *BaseModel) builderWithID() *Builder {
	builder := b.newBuilder()

	if b.__self == nil {
		return builder
	}

	pks, err := db.GetPrimaryKeys(b.__self)
	if err != nil {
		return builder
	}

	for _, pk := range pks {
		if !pk.IsZero {
			builder.WhereEq(pk.ColumnName, pk.Value)
		}
	}

	return builder
}

func (b *BaseModel) Validate() error {
	if b.__res == nil || b.__self == nil {
		return errors.New("model is not initialized properly")
	}

	v := b.__res.Validator(b.__self, true)
	return v.Validate()
}

func (b *BaseModel) Cols(cols ...string) *Builder {
	return b.newBuilder().Cols(cols...)
}

func (b *BaseModel) Nullable(cols ...string) *Builder {
	return b.newBuilder().Nullable(cols...)
}

func (b *BaseModel) WithoutDefaultCondition() *Builder {
	return b.newBuilder().WithoutDefaultCondition()
}

func (b *BaseModel) Where(expr string, args ...any) *Builder {
	return b.newBuilder().Where(expr, args...)
}

func (b *BaseModel) OrWhere(expr string, args ...any) *Builder {
	return b.newBuilder().OrWhere(expr, args...)
}

func (b *BaseModel) WhereEq(col string, val any) *Builder {
	return b.newBuilder().WhereEq(col, val)
}

func (b *BaseModel) WhereIn(col string, vals []any) *Builder {
	return b.newBuilder().WhereIn(col, vals)
}

func (b *BaseModel) WhereNull(col string) *Builder {
	return b.newBuilder().WhereNull(col)
}

func (b *BaseModel) Join(table, on string, typ string) *Builder {
	return b.newBuilder().Join(table, on, typ)
}

func (b *BaseModel) LeftJoin(table, on string) *Builder {
	return b.newBuilder().LeftJoin(table, on)
}

func (b *BaseModel) RightJoin(table, on string) *Builder {
	return b.newBuilder().RightJoin(table, on)
}

func (b *BaseModel) InnerJoin(table, on string) *Builder {
	return b.newBuilder().InnerJoin(table, on)
}

func (b *BaseModel) GroupBy(cols ...string) *Builder {
	return b.newBuilder().GroupBy(cols...)
}

func (b *BaseModel) Having(expr string, args ...any) *Builder {
	return b.newBuilder().Having(expr, args...)
}

func (b *BaseModel) OrderBy(expr string) *Builder {
	return b.newBuilder().OrderBy(expr)
}

func (b *BaseModel) Limit(n int) *Builder {
	return b.newBuilder().Limit(n)
}

func (b *BaseModel) Offset(n int) *Builder {
	return b.newBuilder().Offset(n)
}

func (b *BaseModel) WithContext(ctx context.Context) *Builder {
	return b.newBuilder().WithContext(ctx)
}

func (b *BaseModel) Use(tx Tx) *Builder {
	return b.newBuilder().Use(tx)
}

func (b *BaseModel) Insert() (Result, error) {
	return b.newBuilder().Insert()
}

func (b *BaseModel) Upsert(updateCols ...string) (Result, error) {
	return b.newBuilder().Upsert(b.__self, updateCols...)
}

func (b *BaseModel) Update() (Result, error) {
	return b.builderWithID().Update()
}

func (b *BaseModel) UpdateAll() (Result, error) {
	return b.newBuilder().UpdateAll(b.__self)
}

func (b *BaseModel) Delete() (Result, error) {
	return b.builderWithID().Delete()
}

func (b *BaseModel) DeleteAll() (Result, error) {
	return b.newBuilder().DeleteAll(b.__self)
}

func (b *BaseModel) Purge() (Result, error) {
	builder := b.builderWithID()
	builder.ClearSoftDeleteCondition()
	return builder.Delete()
}

func (b *BaseModel) PurgeAll() (Result, error) {
	builder := b.newBuilder()
	builder.ClearSoftDeleteCondition()
	return builder.DeleteAll(b.__self)
}

func (b *BaseModel) Find() (bool, error) {
	return b.newBuilder().Find()
}

func (b *BaseModel) FindAll() error {
	return b.newBuilder().FindAll()
}

func (b *BaseModel) Paginate(page, perPage int) (*db.Pagination, error) {
	return b.newBuilder().Paginate(b.__self, page, perPage)
}

func (b *BaseModel) Count() (int64, error) {
	return b.newBuilder().Count()
}

func (b *BaseModel) Exist() (bool, error) {
	return b.newBuilder().Exist()
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
	return b.__db.Raw(query, args...).SetResources(b.__res)
}

func (b *BaseModel) Table(name string) *Builder {
	if b.__db == nil {
		panic("kiya: model is not initialized, database is nil")
	}
	return b.__db.Table(name).SetResources(b.__res)
}

func (b *BaseModel) Stats() sql.DBStats {
	if b.__db == nil {
		return sql.DBStats{}
	}
	return b.__db.Stats()
}
