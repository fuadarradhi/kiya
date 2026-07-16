package db

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
)

func (b *Builder) Find(dest ...any) (bool, error) {
	b.applyScope()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return false, ErrDestinationNil
	}

	clone := b.clone()

	if clone.rawQuery == "" {
		clone.Limit(1)

		val := reflect.ValueOf(d)
		if val.Kind() != reflect.Ptr || val.IsNil() {
			return false, ErrModelNil
		}
		valElem := val.Elem()
		typ := valElem.Type()

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
				clone.table = SanitizeIdentifier(info.defaultName)
			}
		} else {
			if clone.table == "" {
				tblName, err := getTableNameFromModel(d)
				if err != nil {
					return false, fmt.Errorf("Find: %v", err)
				}
				clone.table = SanitizeIdentifier(tblName)
			}
		}

		if clone.table == "" {
			return false, ErrTableRequired
		}
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

func (b *Builder) FindOrFail(dest ...any) error {
	found, err := b.Find(dest...)
	if err != nil {
		return err
	}
	if !found {
		return notFoundErr()
	}
	return nil
}

func (b *Builder) FindAll(dest ...any) error {
	b.applyScope()

	var d any
	if len(dest) > 0 {
		d = dest[0]
	} else {
		d = b.dest
	}

	if d == nil {
		return ErrDestinationNil
	}

	clone := b.clone()

	if clone.rawQuery == "" {
		sliceVal := reflect.ValueOf(d)
		if sliceVal.Kind() != reflect.Ptr || sliceVal.Elem().Kind() != reflect.Slice {
			if clone.table == "" {
				tblName, err := getTableNameFromModel(d)
				if err != nil {
					return fmt.Errorf("FindAll: %v", err)
				}
				clone.table = SanitizeIdentifier(tblName)
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
					clone.table = SanitizeIdentifier(info.defaultName)
				}
			}
		}

		if clone.table == "" {
			return ErrTableRequired
		}
	}

	query, args := clone.Build()
	return clone.executor.Select(clone.ctx, d, query, args...)
}

func (b *Builder) Count() (int64, error) {
	b.applyScope()

	clone := b.clone()
	clone.selects = nil
	clone.Cols("COUNT(*)")

	if clone.table == "" {
		return 0, ErrTableRequired
	}

	query, args := clone.Build()
	var count int64
	err := clone.executor.Get(clone.ctx, &count, query, args...)
	return count, err
}

func (b *Builder) Exist() (bool, error) {
	b.applyScope()

	clone := b.clone()
	clone.selects = nil
	clone.Cols("1")
	clone.Limit(1)

	if clone.table == "" {
		return false, ErrTableRequired
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

func (b *Builder) Paginate(dest any, page, perPage int) (*Pagination, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 10
	}
	if perPage > 100 {
		perPage = 100
	}

	total, err := b.Count()
	if err != nil {
		return nil, err
	}

	lastPage := 0
	if perPage > 0 {
		lastPage = int((total + int64(perPage) - 1) / int64(perPage))
	}

	p := &Pagination{
		Total:    total,
		Page:     page,
		PerPage:  perPage,
		LastPage: lastPage,
	}

	if total == 0 {
		return p, nil
	}

	err = b.Limit(perPage).Offset((page - 1) * perPage).FindAll(dest)
	if err != nil {
		return nil, err
	}

	return p, nil
}

func (b *Builder) Exec() (int64, error) {
	clone := b.clone()
	query, args := clone.Build()
	return resultAffected(clone.executor.Exec(clone.ctx, query, args...))
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
