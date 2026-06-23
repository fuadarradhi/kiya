package db

import (
	"errors"
	"reflect"
)

type PrimaryKeyInfo struct {
	ColumnName string
	Value      any
	IsZero     bool
}

type Pagination struct {
	Total    int64
	Page     int
	PerPage  int
	LastPage int
}

func GetTableNameFromModel(model any) (string, error) {
	return getTableNameFromModel(model)
}

func GetPrimaryKeys(model any) ([]PrimaryKeyInfo, error) {
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil, nil
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil, errors.New("model must be a struct")
	}

	info, err := getStructInfo(val.Type())
	if err != nil {
		return nil, err
	}

	var pks []PrimaryKeyInfo
	if len(info.primaryKeys) > 0 {
		for _, f := range info.fields {
			if f.isPrimary {
				fieldVal := val.Field(f.idx)
				pks = append(pks, PrimaryKeyInfo{
					ColumnName: f.name,
					Value:      fieldVal.Interface(),
					IsZero:     isZero(fieldVal),
				})
			}
		}
	} else {
		for _, f := range info.fields {
			if f.name == "id" {
				fieldVal := val.Field(f.idx)
				pks = append(pks, PrimaryKeyInfo{
					ColumnName: "id",
					Value:      fieldVal.Interface(),
					IsZero:     isZero(fieldVal),
				})
				break
			}
		}
	}
	return pks, nil
}

func (b *Builder) SetSoftDeleteCondition(cond string) {
	b.softDeleteCondition = cond
}

func (b *Builder) ClearSoftDeleteCondition() {
	b.softDeleteCondition = ""
}

func (b *Builder) UpdateAll(data any) (Result, error) {
	return b.execUpdate(data, true)
}

func (b *Builder) DeleteAll(data any) (Result, error) {
	return b.execDelete(data, true)
}
