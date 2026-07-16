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

func PrimaryKeyValue(model any) (int64, error) {
	pks, err := GetPrimaryKeys(model)
	if err != nil {
		return 0, err
	}
	if len(pks) == 0 {
		return 0, errors.New("kiya: no primary key field found on model")
	}
	return toInt64(pks[0].Value)
}

func toInt64(v any) (int64, error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), nil
	default:
		return 0, errors.New("kiya: primary key value is not an integer type")
	}
}

func (b *Builder) SetSoftDeleteCondition(cond string) {
	b.softDeleteCondition = cond
}

func (b *Builder) ClearSoftDeleteCondition() {
	b.softDeleteCondition = ""
}

func (b *Builder) UpdateAll(data any) (int64, error) {
	return b.execUpdate(data, true)
}

func (b *Builder) DeleteAll(data any) (int64, error) {
	return b.execDelete(data, true)
}
