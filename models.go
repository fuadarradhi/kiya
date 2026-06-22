package kiya

import (
	"context"
	"errors"
	"reflect"
)

type BaseModel struct {
	__db            *DB
	__res           *Resources
	__self          any
	__hasSoftDelete bool
}

func (b *BaseModel) Init(db *DB, res *Resources, self any) {
	b.__db = db
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
	name, _ := getTableNameFromModel(b.__self)
	return name
}

func (b *BaseModel) newBuilder() *Builder {
	if b.__db == nil || b.__self == nil {
		return nil
	}
	builder := b.__db.Table(b.TableName()).Bind(b.__self).SetResources(b.__res)

	if b.__hasSoftDelete {
		builder.softDeleteCondition = "deleted_at IS NULL"
	}

	return builder
}

func (b *BaseModel) builderWithID() *Builder {
	builder := b.newBuilder()

	if b.__self == nil {
		return builder
	}

	val := reflect.ValueOf(b.__self)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return builder
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return builder
	}

	typ := val.Type()
	info, err := getStructInfo(typ)
	if err != nil {
		return builder
	}

	if len(info.primaryKeys) > 0 {
		for _, f := range info.fields {
			if f.isPrimary {
				fieldVal := val.Field(f.idx)
				if !isZero(fieldVal) {
					builder.WhereEq(f.name, fieldVal.Interface())
				}
			}
		}
	} else {
		for _, f := range info.fields {
			if f.name == "id" {
				fieldVal := val.Field(f.idx)
				if !isZero(fieldVal) {
					builder.WhereEq("id", fieldVal.Interface())
				}
				break
			}
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
	return b.newBuilder().execUpdate(b.__self, true)
}

func (b *BaseModel) Delete() (Result, error) {
	return b.builderWithID().Delete()
}

func (b *BaseModel) DeleteAll() (Result, error) {
	return b.newBuilder().execDelete(b.__self, true)
}

func (b *BaseModel) Purge() (Result, error) {
	builder := b.builderWithID()
	builder.softDeleteCondition = ""
	return builder.Delete()
}

func (b *BaseModel) PurgeAll() (Result, error) {
	builder := b.newBuilder()
	builder.softDeleteCondition = ""
	return builder.execDelete(b.__self, true)
}

func (b *BaseModel) Find() (bool, error) {
	return b.newBuilder().Find()
}

func (b *BaseModel) FindAll() error {
	return b.newBuilder().FindAll()
}

func (b *BaseModel) Count() (int64, error) {
	return b.newBuilder().Count()
}

func (b *BaseModel) Exist() (bool, error) {
	return b.newBuilder().Exist()
}

func (b *BaseModel) WithDeleted() *Builder {
	builder := b.newBuilder()
	builder.softDeleteCondition = ""
	return builder
}

func (b *BaseModel) Raw(query string, args ...any) *Builder {
	if b.__db == nil {
		return nil
	}
	return b.__db.Raw(query, args...).SetResources(b.__res)
}

func (b *BaseModel) Table(name string) *Builder {
	if b.__db == nil {
		return nil
	}
	return b.__db.Table(name).SetResources(b.__res)
}
