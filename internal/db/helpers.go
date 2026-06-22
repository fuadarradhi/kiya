package db

import (
	"errors"
	"reflect"
)

// PrimaryKeyInfo holds data about a primary key field in a struct.
type PrimaryKeyInfo struct {
	ColumnName string
	Value      any
	IsZero     bool
}

// GetTableNameFromModel extracts the table name from a model struct or slice.
func GetTableNameFromModel(model any) (string, error) {
	return getTableNameFromModel(model)
}

// GetPrimaryKeys extracts primary key information from a model struct.
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

// SetSoftDeleteCondition sets the soft delete condition on the builder.
func (b *Builder) SetSoftDeleteCondition(cond string) {
	b.softDeleteCondition = cond
}

// ClearSoftDeleteCondition removes the soft delete condition.
func (b *Builder) ClearSoftDeleteCondition() {
	b.softDeleteCondition = ""
}

// UpdateAll executes an update without requiring a where clause.
func (b *Builder) UpdateAll(data any) (Result, error) {
	return b.execUpdate(data, true)
}

// DeleteAll executes a delete without requiring a where clause.
func (b *Builder) DeleteAll(data any) (Result, error) {
	return b.execDelete(data, true)
}
