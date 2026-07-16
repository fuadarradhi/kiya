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
	historyExclude  bool
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
		case "__db", "__res", "__self", "__hasSoftDelete", "__tx":
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

		if field.Tag.Get("history") == "-" {
			fInfo.historyExclude = true
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

func PrimaryKeyColumn(model any) (string, error) {
	pks, err := GetPrimaryKeys(model)
	if err != nil {
		return "", err
	}
	if len(pks) > 0 {
		return pks[0].ColumnName, nil
	}
	return "id", nil
}

func SetPrimaryKeyValue(model any, id int64) error {
	val := reflect.ValueOf(model)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return errors.New("model must be a non-nil pointer")
	}
	val = val.Elem()
	if val.Kind() != reflect.Struct {
		return errors.New("model must point to a struct")
	}

	info, err := getStructInfo(val.Type())
	if err != nil {
		return err
	}

	var target *dbCachedField
	for i := range info.fields {
		if info.fields[i].isPrimary {
			target = &info.fields[i]
			break
		}
	}
	if target == nil {
		for i := range info.fields {
			if info.fields[i].name == "id" {
				target = &info.fields[i]
				break
			}
		}
	}
	if target == nil {
		return errors.New("no primary key field found on model")
	}

	fieldVal := val.Field(target.idx)
	if !fieldVal.CanSet() {
		return errors.New("primary key field is not settable")
	}

	switch fieldVal.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fieldVal.SetInt(id)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		fieldVal.SetUint(uint64(id))
	default:
		return errors.New("primary key field is not an integer type")
	}
	return nil
}

func StructToHistoryMap(model any, cols []string) (map[string]any, error) {
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

	info, err := getStructInfo(val.Type())
	if err != nil {
		return nil, err
	}

	colsSet := make(map[string]bool)
	for _, c := range cols {
		colsSet[c] = true
	}

	data := make(map[string]any)
	for _, f := range info.fields {
		if f.historyExclude || f.isPrimary || f.isAutoincrement {
			continue
		}
		if len(colsSet) > 0 && !colsSet[f.name] {
			continue
		}
		data[f.name] = val.Field(f.idx).Interface()
	}
	return data, nil
}

func columnsForUpsert(model any) ([]string, error) {
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil, errors.New("model is nil")
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

	var cols []string
	for _, f := range info.fields {
		if f.isPrimary || f.isAutoincrement || f.isAutofill {
			continue
		}
		cols = append(cols, f.name)
	}
	return cols, nil
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

func structToMap(model any, selectedCols []string) (map[string]any, error) {
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

		if f.isNullable {
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
		switch val.Kind() {
		case reflect.Int64:
			field.SetInt(val.Int())
		case reflect.Uint64:
			field.SetInt(int64(val.Uint()))
		case reflect.Float64:
			field.SetInt(int64(val.Float()))
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch val.Kind() {
		case reflect.Uint64:
			field.SetUint(val.Uint())
		case reflect.Int64:
			field.SetUint(uint64(val.Int()))
		case reflect.Float64:
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

	case reflect.Ptr:
		elem := reflect.New(field.Type().Elem())
		setFieldValue(elem.Elem(), value)
		field.Set(elem)

	default:
		if val.Type().ConvertibleTo(field.Type()) {
			field.Set(val.Convert(field.Type()))
		}
	}
}
