package kiya

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

func isValidRedirectCode(code int) bool {
	return code >= 300 && code <= 399
}

func sanitizeForJSON(v any) any {
	if v == nil {
		return nil
	}

	rv := reflect.ValueOf(v)

	for (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface) && !rv.IsNil() {
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.Bool,
		reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return rv.Interface()
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		result := make(map[string]any)
		iter := rv.MapRange()
		for iter.Next() {
			key := fmt.Sprintf("%v", iter.Key().Interface())
			result[key] = sanitizeForJSON(iter.Value().Interface())
		}
		return result
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = sanitizeForJSON(rv.Index(i).Interface())
		}
		return result
	case reflect.Struct:
		if t, ok := rv.Interface().(time.Time); ok {
			if t.IsZero() {
				return nil
			}
			return t.Format(time.RFC3339)
		}
		result := make(map[string]any)
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			jsonTag := field.Tag.Get("json")
			if jsonTag == "-" {
				continue
			}
			fieldName := field.Name
			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if len(parts) > 0 && parts[0] != "" && parts[0] != "-" {
					fieldName = parts[0]
				}
			}
			if rv.Field(i).CanInterface() {
				result[fieldName] = sanitizeForJSON(rv.Field(i).Interface())
			}
		}
		return result
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil
	default:
		return nil
	}
}
