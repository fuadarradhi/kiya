package db

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/util"
)

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

func getStructInfo(typ reflect.Type) (*dbCachedStruct, error) {
	if cached, ok := dbStructCache.Load(typ); ok {
		return cached.(*dbCachedStruct), nil
	}

	info := &dbCachedStruct{}
	info.defaultName = util.ToSnakeCase(typ.Name())

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
				logger.LogWarn("[DB] Multiple autoincrement fields in struct %s, using first: %s", typ.Name(), info.autoIncCol)
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

	// Fallback: field dengan tag db bernama "id" (case-insensitive),
	// untuk struct yang tidak menandai autoincrement secara eksplisit.
	for _, f := range info.fields {
		if strings.EqualFold(f.name, "id") {
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
