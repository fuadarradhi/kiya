package kiya

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuadarradhi/kiya/internal/util"
)

// RulesFunc defines the validation function signature.
type RulesFunc func(val any) error

var (
	validator   map[string]func(v *Validator, param string) RulesFunc
	validatorMu sync.RWMutex
)

var validColumnNameRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Validator handles struct validation and error formatting.
type Validator struct {
	res            *Resources
	globalErrors   []error
	values         Map
	validateErrors map[string][]string
	uniqueTable    string
	parsedRules    map[string]map[string]string
	customFuncs    map[string][]RulesFunc

	pkCol string
	pkVal any
}

func init() {
	validator = map[string]func(v *Validator, param string) RulesFunc{
		"required":  valRequired,
		"email":     valEmail,
		"length":    valLength,
		"op_length": valOpLength,
		"maxlength": valMaxLength,
		"minlength": valMinLength,
		"url":       valUrl,
		"numeric":   valNumeric,
		"unique":    valUnique,
		"secpass":   valSecPass,
	}
}

// RegisterRules registers a custom validation rule.
func RegisterRules(name string, fn func(v *Validator, param string) RulesFunc) {
	validatorMu.Lock()
	defer validatorMu.Unlock()

	if _, exists := validator[name]; exists {
		LogWarn("[Validator] Rule '%s' already exists, it will be overwritten", name)
	}

	validator[name] = fn
}

// RegisterSimpleRule registers a simple validation rule without parameters.
func RegisterSimpleRule(name string, fn func(val any) error) {
	RegisterRules(name, func(v *Validator, param string) RulesFunc {
		return fn
	})
}

// HasRule checks if a validation rule exists.
func HasRule(name string) bool {
	validatorMu.RLock()
	defer validatorMu.RUnlock()
	_, ok := validator[name]
	return ok
}

func (v *Validator) SetTable(table string) *Validator {
	if !validColumnNameRegex.MatchString(table) {
		LogWarn("[Validator] Invalid table name rejected: %s", table)
		return v
	}
	v.uniqueTable = table
	return v
}

func (v *Validator) Bind(form any, bind ...bool) *Validator {
	shouldBind := true
	if len(bind) > 0 {
		shouldBind = bind[0]
	}

	if shouldBind {
		if err := v.res.Bind(form); err != nil {
			v.globalErrors = append(v.globalErrors, err)
		}
	}

	if v.values == nil {
		v.values = make(Map)
	}
	if v.parsedRules == nil {
		v.parsedRules = make(map[string]map[string]string)
	}
	if v.customFuncs == nil {
		v.customFuncs = make(map[string][]RulesFunc)
	}

	for k := range v.values {
		delete(v.values, k)
	}

	val := reflect.ValueOf(form)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	v.pkCol = ""
	v.pkVal = nil

	if v.uniqueTable == "" {
		if val.CanAddr() {
			if tn, ok := val.Addr().Interface().(interface{ TableName() string }); ok {
				v.uniqueTable = tn.TableName()
			}
		}

		if v.uniqueTable == "" {
			v.uniqueTable = util.ToSnakeCase(val.Type().Name())
		}
	}

	if val.Kind() == reflect.Struct {
		typ := val.Type()
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			dbTag := field.Tag.Get("db")
			if dbTag == "" {
				continue
			}

			parts := strings.Split(dbTag, ",")
			colName := parts[0]

			isPrimary := false
			for _, p := range parts[1:] {
				if p == "primary" {
					isPrimary = true
					break
				}
			}

			if isPrimary {
				if validColumnNameRegex.MatchString(colName) {
					v.pkCol = colName
				} else {
					LogWarn("[Validator] Invalid primary key column name rejected: %s", colName)
				}
				fieldVal := val.Field(i)
				if fieldVal.CanInterface() {
					v.pkVal = fieldVal.Interface()
				}
				break
			}
		}
	}

	if val.Kind() == reflect.Struct {
		v.flattenStruct(val, "")
	}

	return v
}

func valUnique(v *Validator, param string) RulesFunc {
	return func(val any) error {
		if v.uniqueTable == "" {
			return errors.New("validator configuration: table not set")
		}

		if !validColumnNameRegex.MatchString(param) {
			return fmt.Errorf("invalid column name: %s", param)
		}

		if v.res.Database() == nil {
			return errors.New("database not available")
		}

		var found bool
		var err error

		builder := v.res.Database().Table(v.uniqueTable).Where("deleted_at IS NULL")

		if v.pkCol != "" && v.pkVal != nil && !reflect.ValueOf(v.pkVal).IsZero() {
			if !validColumnNameRegex.MatchString(v.pkCol) {
				return errors.New("validator configuration: invalid primary key column")
			}
			found, err = builder.
				Where(fmt.Sprintf("%s != ? AND %s = ?", v.pkCol, param), v.pkVal, val).
				Exist()
		} else {
			found, err = builder.
				Where(fmt.Sprintf("%s = ?", param), val).
				Exist()
		}

		if err != nil {
			LogError("[Validator] Unique check DB error: %v", err)
			return errors.New("failed to validate data")
		}

		if found {
			return errors.New("already in use")
		}
		return nil
	}
}

func (v *Validator) ResetRules() *Validator {
	v.parsedRules = make(map[string]map[string]string)
	v.customFuncs = make(map[string][]RulesFunc)
	return v
}

func (v *Validator) flattenStruct(val reflect.Value, prefix string) {
	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		fieldVal := val.Field(i)
		fieldType := typ.Field(i)

		if !fieldVal.CanInterface() {
			continue
		}

		key := fieldType.Tag.Get("form")
		if key == "" {
			key = fieldType.Name
		}
		if key == "-" {
			continue
		}

		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}

		if fieldVal.Kind() == reflect.Ptr {
			if !fieldVal.IsNil() {
				dereferencedVal := fieldVal.Elem()
				if dereferencedVal.Kind() == reflect.Struct && dereferencedVal.Type() != reflect.TypeOf(time.Time{}) {
					v.flattenStruct(dereferencedVal, fullKey)
					continue
				}
			} else {
				continue
			}
		}

		if fieldVal.Kind() == reflect.Struct && fieldType.Type != reflect.TypeOf(time.Time{}) {
			v.flattenStruct(fieldVal, fullKey)
		} else {
			v.values[fullKey] = fieldVal.Interface()

			tagRules := fieldType.Tag.Get("validate")
			if tagRules != "" {
				v.mergeRules(fullKey, tagRules)
			}
		}
	}
}

func (v *Validator) mergeRules(field string, rulesStr string) {
	if v.parsedRules[field] == nil {
		v.parsedRules[field] = make(map[string]string)
	}

	lsRules := strings.Split(rulesStr, "|")
	for _, rule := range lsRules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}

		var ruleName string
		var ruleParam string

		if strings.Contains(rule, "[") && strings.HasSuffix(rule, "]") {
			parts := strings.SplitN(rule, "[", 2)
			ruleName = parts[0]
			ruleParam = strings.TrimSuffix(parts[1], "]")
		} else {
			ruleName = rule
		}

		v.parsedRules[field][ruleName] = ruleParam
	}
}

func (v *Validator) Rules(field string, rules string, fn ...RulesFunc) *Validator {
	v.mergeRules(field, rules)

	if len(fn) > 0 {
		v.Func(field, fn...)
	}

	return v
}

func (v *Validator) Func(field string, fn ...RulesFunc) *Validator {
	if v.customFuncs == nil {
		v.customFuncs = make(map[string][]RulesFunc)
	}
	v.customFuncs[field] = append(v.customFuncs[field], fn...)
	return v
}

func (v *Validator) Validate() error {
	if len(v.globalErrors) > 0 {
		if v.validateErrors == nil {
			v.validateErrors = make(map[string][]string)
		}
		for i, err := range v.globalErrors {
			v.addError(fmt.Sprintf("binding_error_%d", i), err.Error())
		}
		return v.Errors()
	}

	for field, rulesMap := range v.parsedRules {
		val := v.values[field]

		for ruleName, ruleParam := range rulesMap {
			validatorMu.RLock()
			ruleFunc, ok := validator[ruleName]
			validatorMu.RUnlock()

			if ok {
				fn := ruleFunc(v, ruleParam)
				if err := fn(val); err != nil {
					v.addError(field, err.Error())
				}
			} else {
				LogWarn("[Validator] Rule '%s' not found for field '%s'", ruleName, field)
			}
		}
	}

	for field, fns := range v.customFuncs {
		val := v.values[field]
		for _, fn := range fns {
			if err := fn(val); err != nil {
				v.addError(field, err.Error())
			}
		}
	}

	if len(v.validateErrors) > 0 {
		v.Errors()
		return errors.New("validation failed")
	}

	return nil
}

func (v *Validator) addError(field string, msg string) {
	if v.validateErrors == nil {
		v.validateErrors = make(map[string][]string)
	}
	v.validateErrors[field] = append(v.validateErrors[field], msg)
}

func (v *Validator) Error(field string, err string) *Validator {
	v.addError(field, err)
	return v
}

func (v *Validator) Errors() error {
	return v.res.APIResponse(http.StatusUnprocessableEntity,
		"there are errors in your input, please check and try again",
		v.validateErrors, []string{},
	)
}

func valRequired(v *Validator, param string) RulesFunc {
	return func(val any) error {
		if str, ok := val.(string); ok {
			if len(str) == 0 {
				return errors.New("is required")
			}
		}

		if t, ok := val.(time.Time); ok {
			if t.IsZero() {
				return errors.New("is required")
			}
		}

		if _, ok := val.(bool); ok {
			return nil
		}

		if val == nil {
			return errors.New("is required")
		}

		rv := reflect.ValueOf(val)
		if (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface) && rv.IsNil() {
			return errors.New("is required")
		}

		if isEmpty(val) {
			return errors.New("is required")
		}

		return nil
	}
}

func valSecPass(v *Validator, param string) RulesFunc {
	return func(val any) error {
		str, ok := val.(string)
		if !ok {
			return errors.New("invalid password format")
		}

		if len(str) < 8 {
			return errors.New("password must be at least 8 characters")
		}

		var (
			hasUpper  bool
			hasLower  bool
			hasNumber bool
			hasSymbol bool
		)

		for _, c := range str {
			switch {
			case 'A' <= c && c <= 'Z':
				hasUpper = true
			case 'a' <= c && c <= 'z':
				hasLower = true
			case '0' <= c && c <= '9':
				hasNumber = true
			case strings.ContainsRune("!@#$%^&*()-_=+[]{}|;:'\",.<>?/`~", c):
				hasSymbol = true
			}
		}

		if !hasUpper || !hasLower || !hasNumber || !hasSymbol {
			return errors.New("password must contain uppercase, lowercase, numbers, and symbols")
		}

		return nil
	}
}

func valEmail(v *Validator, param string) RulesFunc {
	return func(val any) error {
		if isEmpty(val) {
			return nil
		}

		str, ok := val.(string)
		if !ok {
			return errors.New("invalid email format")
		}

		e, err := mail.ParseAddress(str)
		if err != nil {
			return errors.New("email is not valid")
		}

		if e.Address == "" {
			return errors.New("email is not valid")
		}

		return nil
	}
}

func valUrl(v *Validator, param string) RulesFunc {
	return func(val any) error {
		if isEmpty(val) {
			return nil
		}

		str, ok := val.(string)
		if !ok {
			return errors.New("invalid url")
		}

		u, err := url.ParseRequestURI(str)
		if err != nil {
			return errors.New("invalid url")
		}

		allowedSchemes := map[string]bool{
			"http":   true,
			"https":  true,
			"ftp":    true,
			"mailto": true,
		}

		if !allowedSchemes[u.Scheme] {
			return errors.New("invalid url: scheme not allowed")
		}

		return nil
	}
}

func valNumeric(v *Validator, param string) RulesFunc {
	return func(val any) error {
		if isEmpty(val) {
			return nil
		}

		rv := reflect.ValueOf(val)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64:
			return nil
		}

		if str, ok := val.(string); ok {
			if _, err := strconv.ParseFloat(str, 64); err != nil {
				return errors.New("must be a number")
			}
		} else {
			return errors.New("must be a number")
		}
		return nil
	}
}

func valLength(v *Validator, param string) RulesFunc {
	return func(val any) error {
		str, ok := val.(string)
		if !ok {
			return nil
		}

		if len(str) == 0 {
			return nil
		}

		p, err := strconv.Atoi(param)
		if err != nil {
			return nil
		}

		if len(str) != p {
			return fmt.Errorf("must be %d characters", p)
		}
		return nil
	}
}

func valOpLength(v *Validator, param string) RulesFunc {
	return func(val any) error {
		str, ok := val.(string)
		if !ok {
			return nil
		}

		if strings.TrimSpace(str) == "" {
			return nil
		}

		p, err := strconv.Atoi(param)
		if err != nil {
			return nil
		}

		if len(str) != p {
			return fmt.Errorf("must be %d characters", p)
		}
		return nil
	}
}

func valMaxLength(v *Validator, param string) RulesFunc {
	return func(val any) error {
		str, ok := val.(string)
		if !ok {
			return nil
		}

		if len(str) == 0 {
			return nil
		}

		p, err := strconv.Atoi(param)
		if err != nil {
			return nil
		}

		if len(str) > p {
			return fmt.Errorf("maximum %d characters", p)
		}
		return nil
	}
}

func valMinLength(v *Validator, param string) RulesFunc {
	return func(val any) error {
		str, ok := val.(string)
		if !ok {
			return nil
		}

		if len(str) == 0 {
			return nil
		}

		p, err := strconv.Atoi(param)
		if err != nil {
			return nil
		}

		if len(str) < p {
			return fmt.Errorf("minimum %d characters", p)
		}
		return nil
	}
}

func isEmpty(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.String, reflect.Array, reflect.Map, reflect.Slice:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Invalid:
		return true
	case reflect.Interface, reflect.Ptr:
		if v.IsNil() {
			return true
		}
		return isEmpty(v.Elem().Interface())
	case reflect.Struct:
		if t, ok := value.(time.Time); ok {
			return t.IsZero()
		}
		return false
	}

	return false
}
