package decoder

import (
	"reflect"
	"sort"
	"strings"
	"sync"
)

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
