package decoder

import (
	"bytes"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

var (
	FormDecoder       *Decoder
	strictPolicy      *bluemonday.Policy
	ugcPolicy         *bluemonday.Policy
	spaceRegex        = regexp.MustCompile(`\s+`)
	dangerousTagRegex = regexp.MustCompile(`(?is)(?:<script[^>]*>.*?</script>|<style[^>]*>.*?</style>)`)
)

func init() {
	strictPolicy = bluemonday.StrictPolicy()
	ugcPolicy = bluemonday.UGCPolicy()
	FormDecoder = NewDecoder()
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
