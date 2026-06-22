package kiya

import (
	"bytes"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/araddon/dateparse"
	"github.com/microcosm-cc/bluemonday"
)

var (
	formDecoder       *Decoder
	strictPolicy      *bluemonday.Policy
	ugcPolicy         *bluemonday.Policy
	spaceRegex        = regexp.MustCompile(`\s+`)
	dangerousTagRegex = regexp.MustCompile(`(?is)(?:<script[^>]*>.*?</script>|<style[^>]*>.*?</style>)`)
)

func init() {
	strictPolicy = bluemonday.StrictPolicy()
	ugcPolicy = bluemonday.UGCPolicy()
	formDecoder = NewDecoder()
}

type Mode uint8

const (
	ModeImplicit Mode = iota
	ModeExplicit
)

type SanitizeMode uint8

const (
	SanitizeDefault SanitizeMode = iota
	SanitizeHTML
	SanitizeRaw
)

type DecodeCustomTypeFunc func([]string) (any, error)

type DecodeErrors map[string]error

func (d DecodeErrors) Error() string {
	buff := bytes.NewBufferString("")
	for k, err := range d {
		buff.WriteString("Field '")
		buff.WriteString(k)
		buff.WriteString("': ")
		buff.WriteString(err.Error())
		buff.WriteString("\n")
	}
	return strings.TrimSpace(buff.String())
}

type InvalidDecoderError struct {
	Type reflect.Type
}

func (e *InvalidDecoderError) Error() string {
	if e.Type == nil {
		return "form: Decode(nil)"
	}
	if e.Type.Kind() != reflect.Ptr {
		return "form: Decode(non-pointer " + e.Type.String() + ")"
	}
	return "form: Decode(nil " + e.Type.String() + ")"
}

type Decoder struct {
	tagName         string
	mode            Mode
	structCache     *structCacheMap
	customTypeFuncs map[reflect.Type]DecodeCustomTypeFunc
	maxArraySize    int
	dataPool        *sync.Pool
	namespacePrefix string
	namespaceSuffix string
}

func NewDecoder() *Decoder {
	d := &Decoder{
		tagName:         "form",
		mode:            ModeImplicit,
		structCache:     newStructCacheMap(),
		maxArraySize:    1000,
		namespacePrefix: ".",
	}

	d.dataPool = &sync.Pool{New: func() any {
		return &decoderContext{
			d:         d,
			namespace: make([]byte, 0, 64),
		}
	}}

	return d
}

func (d *Decoder) RegisterTagNameFunc(fn TagNameFunc) {
	d.structCache.tagFn = fn
}

func (d *Decoder) RegisterCustomTypeFunc(fn DecodeCustomTypeFunc, types ...any) {
	if d.customTypeFuncs == nil {
		d.customTypeFuncs = map[reflect.Type]DecodeCustomTypeFunc{}
	}
	for _, t := range types {
		d.customTypeFuncs[reflect.TypeOf(t)] = fn
	}
}

func (d *Decoder) SetMaxArraySize(size uint) {
	d.maxArraySize = int(size)
}

func (d *Decoder) Decode(v any, values url.Values) (err error) {
	val := reflect.ValueOf(v)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return &InvalidDecoderError{reflect.TypeOf(v)}
	}

	dec := d.dataPool.Get().(*decoderContext)
	dec.values = values
	dec.dm = dec.dm[0:0]
	dec.errs = nil

	val = val.Elem()
	typ := val.Type()

	timeType := reflect.TypeOf(time.Time{})

	if val.Kind() == reflect.Struct && typ != timeType {
		dec.traverseStruct(val, typ, dec.namespace[0:0])
	} else {
		dec.setFieldByType(val, dec.namespace[0:0], 0, cachedField{})
	}

	if len(dec.errs) > 0 {
		err = dec.errs
	}

	const maxNsCap = 256
	const maxDmCap = 200

	if cap(dec.namespace) > maxNsCap {
		dec.namespace = make([]byte, 0, 64)
	} else {
		dec.namespace = dec.namespace[:0]
	}

	if cap(dec.dm) > maxDmCap {
		dec.dm = make(dataMap, 0)
	} else {
		for i := range dec.dm {
			dec.dm[i] = nil
		}
		dec.dm = dec.dm[:0]
	}

	dec.values = nil

	d.dataPool.Put(dec)
	return
}

type TagNameFunc func(field reflect.StructField) string

type cachedField struct {
	idx          int
	name         string
	isAnonymous  bool
	sanitizeMode SanitizeMode
}

type cachedStruct struct {
	fields []cachedField
}

type cacheFields []cachedField

func (s cacheFields) Len() int           { return len(s) }
func (s cacheFields) Less(i, j int) bool { return !s[i].isAnonymous }
func (s cacheFields) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type structCacheMap struct {
	m     sync.Map
	tagFn TagNameFunc
}

func newStructCacheMap() *structCacheMap {
	return new(structCacheMap)
}

func (s *structCacheMap) Get(key reflect.Type) (value *cachedStruct, ok bool) {
	v, ok := s.m.Load(key)
	if !ok {
		return nil, false
	}
	return v.(*cachedStruct), true
}

func (s *structCacheMap) Set(key reflect.Type, value *cachedStruct) {
	s.m.Store(key, value)
}

func (s *structCacheMap) parseStruct(mode Mode, current reflect.Value, key reflect.Type, tagName string) *cachedStruct {
	if cs, ok := s.Get(key); ok {
		return cs
	}

	typ := current.Type()
	cs := &cachedStruct{fields: make([]cachedField, 0, 4)}

	for i := 0; i < current.NumField(); i++ {
		fld := typ.Field(i)

		if fld.PkgPath != "" && !fld.Anonymous {
			continue
		}

		tagVal := fld.Tag.Get(tagName)

		if mode == ModeExplicit && tagVal == "" {
			continue
		}

		name := tagVal
		sanitizeMode := SanitizeDefault

		if tagVal != "" {
			parts := strings.Split(tagVal, ",")

			name = strings.TrimSpace(parts[0])

			if len(parts) > 1 {
				cfg := strings.ToLower(strings.TrimSpace(parts[1]))
				switch cfg {
				case "none", "raw", "safe":
					sanitizeMode = SanitizeRaw
				case "wysiwyg", "html":
					sanitizeMode = SanitizeHTML
				default:
				}
			}
		}

		if name == "-" {
			continue
		}

		if len(name) == 0 {
			name = fld.Name
		}

		cs.fields = append(cs.fields, cachedField{
			idx: i, name: name, isAnonymous: fld.Anonymous,
			sanitizeMode: sanitizeMode,
		})
	}

	sort.Sort(cacheFields(cs.fields))

	actual, _ := s.m.LoadOrStore(typ, cs)
	return actual.(*cachedStruct)
}

type key struct {
	ivalue      int
	value       string
	searchValue string
}

type recursiveData struct {
	alias    string
	sliceLen int
	keys     []key
}

type dataMap []*recursiveData

type decoderContext struct {
	d         *Decoder
	errs      DecodeErrors
	dm        dataMap
	values    url.Values
	maxKeyLen int
	namespace []byte
	depth     int
}

const maxDecodeDepth = 50

func (d *decoderContext) setError(namespace []byte, err error) {
	if d.errs == nil {
		d.errs = make(DecodeErrors)
	}
	d.errs[string(namespace)] = err
}

func (d *decoderContext) findAlias(ns string) *recursiveData {
	for i := 0; i < len(d.dm); i++ {
		if d.dm[i].alias == ns {
			return d.dm[i]
		}
	}
	return nil
}

func (d *decoderContext) parseMapData() {
	if len(d.dm) > 0 {
		return
	}

	d.maxKeyLen = 0
	d.dm = d.dm[0:0]

	for k := range d.values {
		if len(k) > d.maxKeyLen {
			d.maxKeyLen = len(k)
		}

		var idx, l int
		var insideBracket bool
		var isNum bool

		for i := 0; i < len(k); i++ {
			switch k[i] {
			case '[':
				idx = i
				insideBracket = true
				isNum = true
			case ']':
				if !insideBracket {
					LogWarn("[Decoder] Invalid format missing '[' for key %s", k)
					continue
				}

				var rd *recursiveData

				rd = d.findAlias(k[:idx])
				if rd == nil {
					l = len(d.dm) + 1
					if l > cap(d.dm) {
						dm := make(dataMap, l)
						copy(dm, d.dm)
						d.dm = dm
					} else {
						d.dm = d.dm[:l]
					}

					if d.dm[l-1] == nil {
						d.dm[l-1] = &recursiveData{}
					}
					rd = d.dm[l-1]
					rd.sliceLen = 0
					rd.keys = rd.keys[0:0]
					rd.alias = k[:idx]
				}

				ke := key{
					ivalue:      -1,
					value:       k[idx+1 : i],
					searchValue: k[idx : i+1],
				}

				if isNum {
					ke.ivalue, _ = strconv.Atoi(ke.value)
					if ke.ivalue > rd.sliceLen {
						rd.sliceLen = ke.ivalue
					}
				}

				rd.keys = append(rd.keys, ke)
				insideBracket = false
			default:
				if insideBracket && (k[i] > '9' || k[i] < '0') {
					isNum = false
				}
			}
		}

		if insideBracket {
			LogWarn("[Decoder] Invalid format missing ']' for key %s", k)
		}
	}
}

func (d *decoderContext) traverseStruct(v reflect.Value, typ reflect.Type, namespace []byte) (set bool) {
	if d.depth > maxDecodeDepth {
		d.setError(namespace, fmt.Errorf("maximum recursion depth exceeded"))
		return false
	}
	d.depth++
	defer func() { d.depth-- }()

	l := len(namespace)
	first := l == 0

	s, ok := d.d.structCache.Get(typ)
	if !ok {
		s = d.d.structCache.parseStruct(d.d.mode, v, typ, d.d.tagName)
	}

	for _, f := range s.fields {
		namespace = namespace[:l]

		if f.isAnonymous {
			if d.setFieldByType(v.Field(f.idx), namespace, 0, f) {
				set = true
			}
		}

		if first {
			namespace = append(namespace, f.name...)
		} else {
			namespace = append(namespace, d.d.namespacePrefix...)
			namespace = append(namespace, f.name...)
			namespace = append(namespace, d.d.namespaceSuffix...)
		}

		if d.setFieldByType(v.Field(f.idx), namespace, 0, f) {
			set = true
		}
	}
	return
}

func (d *decoderContext) setFieldByType(current reflect.Value, namespace []byte, idx int, field cachedField) (set bool) {
	if !current.CanSet() {
		return false
	}

	v, kind := extractType(current)

	if !v.IsValid() {
		return false
	}

	arr, ok := d.values[string(namespace)]

	formValue := ""
	if ok && idx < len(arr) {
		formValue = cleanValue(arr[idx], field.sanitizeMode)
	}

	if d.d.customTypeFuncs != nil && ok {
		if cf, exists := d.d.customTypeFuncs[v.Type()]; exists {
			val, err := cf(arr[idx:])
			if err != nil {
				d.setError(namespace, err)
				return
			}
			v.Set(reflect.ValueOf(val))
			set = true
			return
		}
	}

	switch kind {
	case reflect.Interface:
		if !ok || idx == len(arr) {
			return
		}
		val := reflect.ValueOf(formValue)
		if val.Type().AssignableTo(v.Type()) {
			v.Set(val)
			set = true
		} else {
			if v.Type().NumMethod() > 0 {
				d.setError(namespace, fmt.Errorf("cannot assign string to interface %s", v.Type()))
			} else {
				v.Set(val)
				set = true
			}
		}

	case reflect.Ptr:
		if !current.IsNil() {
			if d.setFieldByType(current.Elem(), namespace, idx, field) {
				set = true
			}
		} else {
			newVal := reflect.New(v.Type().Elem())
			if d.setFieldByType(newVal.Elem(), namespace, idx, field) {
				set = true
				current.Set(newVal)
			}
		}

	case reflect.String:
		if !ok || idx == len(arr) {
			return
		}
		v.SetString(formValue)
		set = true

	case reflect.Uint, reflect.Uint64:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if u64, err := strconv.ParseUint(formValue, 10, 64); err == nil {
			v.SetUint(u64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid uint: %s", formValue))
		}
	case reflect.Uint8:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if u64, err := strconv.ParseUint(formValue, 10, 8); err == nil {
			v.SetUint(u64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid uint8: %s", formValue))
		}
	case reflect.Uint16:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if u64, err := strconv.ParseUint(formValue, 10, 16); err == nil {
			v.SetUint(u64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid uint16: %s", formValue))
		}
	case reflect.Uint32:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if u64, err := strconv.ParseUint(formValue, 10, 32); err == nil {
			v.SetUint(u64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid uint32: %s", formValue))
		}

	case reflect.Int, reflect.Int64:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if i64, err := strconv.ParseInt(formValue, 10, 64); err == nil {
			v.SetInt(i64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid int64: %s", formValue))
		}
	case reflect.Int8:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if i64, err := strconv.ParseInt(formValue, 10, 8); err == nil {
			v.SetInt(i64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid int8: %s", formValue))
		}
	case reflect.Int16:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if i64, err := strconv.ParseInt(formValue, 10, 16); err == nil {
			v.SetInt(i64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid int16: %s", formValue))
		}
	case reflect.Int32:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if i64, err := strconv.ParseInt(formValue, 10, 32); err == nil {
			v.SetInt(i64)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid int32: %s", formValue))
		}

	case reflect.Float32:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if f, err := strconv.ParseFloat(formValue, 32); err == nil {
			v.SetFloat(f)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid float32: %s", formValue))
		}
	case reflect.Float64:
		if !ok || idx == len(arr) || len(formValue) == 0 {
			return
		}
		if f, err := strconv.ParseFloat(formValue, 64); err == nil {
			v.SetFloat(f)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid float64: %s", formValue))
		}

	case reflect.Bool:
		if !ok || idx == len(arr) {
			return
		}
		if b, err := parseBool(formValue); err == nil {
			v.SetBool(b)
			set = true
		} else {
			d.setError(namespace, fmt.Errorf("invalid bool: %s", formValue))
		}

	case reflect.Slice:
		d.parseMapData()

		if ok && len(arr) > 0 {
			var varr reflect.Value
			l := len(arr)
			if v.IsNil() {
				varr = reflect.MakeSlice(v.Type(), l, l)
			} else {
				oldCap := v.Cap()
				newCap := oldCap
				if l > newCap {
					newCap = l
				}
				varr = reflect.MakeSlice(v.Type(), l, newCap)
				reflect.Copy(varr, v)
			}
			for i := 0; i < l; i++ {
				newVal := reflect.New(v.Type().Elem()).Elem()
				if d.setFieldByType(newVal, namespace, i, field) {
					set = true
				}
				varr.Index(i).Set(newVal)
			}
			v.Set(varr)
		}

		if rd := d.findAlias(string(namespace)); rd != nil {
			var varr reflect.Value
			sl := rd.sliceLen + 1
			if sl > d.d.maxArraySize {
				d.setError(namespace, fmt.Errorf("array size %d exceeds max %d", sl, d.d.maxArraySize))
				return
			}

			if v.IsNil() || v.Len() < sl {
				varr = reflect.MakeSlice(v.Type(), sl, sl)
				if !v.IsNil() {
					reflect.Copy(varr, v)
				}
			} else {
				varr = v
			}

			for i := 0; i < len(rd.keys); i++ {
				kv := rd.keys[i]
				if kv.ivalue == -1 || kv.ivalue >= varr.Len() {
					continue
				}
				newVal := reflect.New(varr.Type().Elem()).Elem()
				ns := append(append([]byte{}, namespace...), kv.searchValue...)
				if d.setFieldByType(newVal, ns, 0, field) {
					set = true
					varr.Index(kv.ivalue).Set(newVal)
				}
			}
			if set {
				v.Set(varr)
			}
		}

	case reflect.Struct:

		if v.Type() == reflect.TypeOf(time.Time{}) {
			if !ok || len(formValue) == 0 {
				return
			}

			var t time.Time
			var err error
			var timeSet bool

			loc := time.Local

			layouts := []string{
				"02/01/2006", "2006/01/02",
				"02-01-2006", "2006-01-02",
				"15:04:05", "15:04",
				time.RFC3339,
			}
			for _, layout := range layouts {
				t, err = time.ParseInLocation(layout, formValue, loc)
				if err == nil {
					timeSet = true
					break
				}
			}

			if !timeSet {
				t, err = dateparse.ParseStrict(formValue)
				if err != nil {
					d.setError(namespace, err)
					return
				}

				t = t.In(loc)
			}

			v.Set(reflect.ValueOf(t))
			set = true
			return
		}

		d.parseMapData()

		set = d.traverseStruct(v, v.Type(), namespace)
	}

	return
}

func extractType(current reflect.Value) (reflect.Value, reflect.Kind) {
	switch current.Kind() {
	case reflect.Ptr:
		if current.IsNil() {
			return current, reflect.Ptr
		}
		return extractType(current.Elem())
	case reflect.Interface:
		if current.IsNil() {
			return current, reflect.Interface
		}
		return extractType(current.Elem())
	default:
		return current, current.Kind()
	}
}

func parseBool(str string) (bool, error) {
	switch str {
	case "1", "t", "T", "true", "TRUE", "True", "on", "yes", "ok":
		return true, nil
	case "", "0", "f", "F", "false", "FALSE", "False", "off", "no":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean: %s", str)
}

func cleanValue(val string, mode SanitizeMode) string {
	val = strings.TrimSpace(val)

	switch mode {
	case SanitizeRaw:
		return val

	case SanitizeHTML:
		val = ugcPolicy.Sanitize(val)
		val = spaceRegex.ReplaceAllString(val, " ")
		return strings.TrimSpace(val)

	default:
		val = dangerousTagRegex.ReplaceAllString(val, "")
		val = strictPolicy.Sanitize(val)
		val = spaceRegex.ReplaceAllString(val, " ")
		return strings.TrimSpace(val)
	}
}
