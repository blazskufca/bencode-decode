package bencode

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

type Decoder struct {
	rawBytes []byte
	curToken int
}

const (
	integer   byte = 'i'
	lists     byte = 'l'
	dict      byte = 'd'
	end       byte = 'e'
	colon     byte = ':'
	null      byte = 0
	asciiZero byte = '0'
	asciiNine byte = '9'
)

func NewDecoder(r io.ReadCloser) (Decoder, error) {
	bytes, err := io.ReadAll(r)
	if err != nil {
		return Decoder{}, err
	}
	defer r.Close()
	if len(bytes) == 0 {
		return Decoder{}, io.EOF
	}
	return Decoder{rawBytes: bytes, curToken: 0}, nil
}

func (d *Decoder) curTokenIs() byte {
	if d.curToken >= len(d.rawBytes) {
		return 0
	}
	return d.rawBytes[d.curToken]
}

func (d *Decoder) advance() {
	if d.curToken < len(d.rawBytes) {
		d.curToken++
	}
}

// Decode decodes Bencode encoded data.
func (d *Decoder) Decode(v any) error {
	var results []any

	for d.curToken < len(d.rawBytes) {
		val, err := d.decode()
		if err != nil {
			return err
		}
		results = append(results, val)
	}

	if len(results) == 1 {
		return d.fillStruct(results[0], reflect.ValueOf(v))
	}

	return d.fillStruct(results, reflect.ValueOf(v))
}

func (d *Decoder) decodeString() (string, error) {
	var lengthStr string

	// Read until we reach the colon ':'
	for d.curToken < len(d.rawBytes) && d.curTokenIs() != colon {
		if d.curTokenIs() < asciiZero || d.curTokenIs() > asciiNine {
			return "", fmt.Errorf("invalid character in string length: %c", d.curTokenIs())
		}
		lengthStr += string(d.curTokenIs())
		d.advance()
	}

	if d.curToken >= len(d.rawBytes) {
		return "", fmt.Errorf("unexpected EOF while reading string length")
	}

	d.advance()

	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return "", fmt.Errorf("invalid string length: %s", lengthStr)
	}

	if length < 0 || d.curToken+length > len(d.rawBytes) {
		return "", fmt.Errorf("invalid string length or unexpected EOF")
	}

	data := string(d.rawBytes[d.curToken : d.curToken+length])
	d.curToken += length

	return data, nil
}

func (d *Decoder) decodeInteger() (int, error) {
	d.advance()

	var numStr string

	if d.curTokenIs() == '-' {
		numStr = "-"
		d.advance()
	}

	// Read digits until we hit 'e'
	for d.curToken < len(d.rawBytes) && d.curTokenIs() != end {
		if d.curTokenIs() < asciiZero || d.curTokenIs() > asciiNine {
			return 0, fmt.Errorf("invalid character in integer: %c", d.curTokenIs())
		}
		numStr += string(d.curTokenIs())
		d.advance()
	}

	if d.curToken >= len(d.rawBytes) {
		return 0, fmt.Errorf("unexpected EOF while reading integer")
	}

	d.advance() // Skip the 'e'

	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid integer: %s", numStr)
	}

	return num, nil
}

func (d *Decoder) decodeList() ([]any, error) {
	d.advance() // Skip over the 'l'
	var result []any

	// Read values until we hit 'e'
	for d.curToken < len(d.rawBytes) && d.curTokenIs() != end {
		value, err := d.decode()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}

	if d.curToken >= len(d.rawBytes) {
		return nil, fmt.Errorf("unexpected EOF while reading list")
	}

	d.advance() // Skip the 'e'
	return result, nil
}

func (d *Decoder) decodeDict() (map[string]any, error) {
	d.advance() // Skip over the 'd'
	result := make(map[string]any)
	for d.curToken < len(d.rawBytes) && d.curTokenIs() != end {
		if !(d.curTokenIs() >= asciiZero && d.curTokenIs() <= asciiNine) {
			return nil, fmt.Errorf("dictionary key must be a string")
		}
		key, err := d.decodeString() // Decode the key
		if err != nil {
			return nil, err
		}
		value, err := d.decode() // Decode the value
		if err != nil {
			return nil, err
		}

		result[key] = value
	}

	if d.curToken >= len(d.rawBytes) {
		return nil, fmt.Errorf("unexpected EOF while reading dictionary")
	}

	d.advance() // skip the e

	return result, nil
}

func (d *Decoder) decode() (any, error) {
	if d.curToken >= len(d.rawBytes) {
		return nil, io.EOF
	}

	curToken := d.curTokenIs()
	switch {
	case curToken == null:
		return nil, nil
	case curToken == integer:
		return d.decodeInteger()
	case curToken == lists:
		return d.decodeList()
	case curToken == dict:
		return d.decodeDict()
	case curToken >= asciiZero && curToken <= asciiNine:
		return d.decodeString()
	default:
		return nil, fmt.Errorf("unknown token: %c", curToken)
	}
}

func (d *Decoder) fillStruct(data any, val reflect.Value) error {
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			val.Set(reflect.New(val.Type().Elem()))
		}
		return d.fillStruct(data, val.Elem())
	}

	if dict, ok := data.(map[string]any); !ok {
		return d.setReflectValue(val, data)
	} else {
		if val.Kind() != reflect.Struct {
			return fmt.Errorf("cannot decode dictionary into non-struct type: %v", val.Type())
		}

		t := val.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			fieldVal := val.Field(i)

			if !fieldVal.CanSet() {
				continue // Skip unexported fields
			}

			tagName := parseTag(field)
			if tagName == "-" {
				continue // Skip fields tagged with "-"
			}

			bencodeValue, exists := dict[tagName]
			if !exists {
				continue
			}

			if err := d.setReflectValue(fieldVal, bencodeValue); err != nil {
				return err
			}
		}
	}

	return nil
}

func parseTag(field reflect.StructField) string {
	tag := field.Tag.Get("bencode")
	if tag == "" {
		return field.Name
	}

	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}

	return name
}

func (d *Decoder) setReflectValue(val reflect.Value, data any) error {
	switch val.Kind() {
	case reflect.String:
		if str, ok := data.(string); ok {
			val.SetString(str)
		} else {
			return fmt.Errorf("cannot set string with value of type %T", data)
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if num, ok := data.(int); ok {
			val.SetInt(int64(num))
		} else if str, ok := data.(string); ok {
			if num, err := strconv.ParseInt(str, 10, 64); err == nil {
				val.SetInt(num)
			} else {
				return fmt.Errorf("cannot convert string to int: %v", err)
			}
		} else {
			return fmt.Errorf("cannot set int with value of type %T", data)
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if num, ok := data.(int); ok && num >= 0 {
			val.SetUint(uint64(num))
		} else {
			return fmt.Errorf("cannot set uint with value of type %T", data)
		}

	case reflect.Bool:
		if num, ok := data.(int); ok {
			val.SetBool(num != 0)
		} else {
			return fmt.Errorf("cannot set bool with value of type %T", data)
		}

	case reflect.Float32, reflect.Float64:
		if num, ok := data.(int); ok {
			val.SetFloat(float64(num))
		} else {
			return fmt.Errorf("cannot set float with value of type %T", data)
		}

	case reflect.Slice:
		if list, ok := data.([]any); ok {
			newSlice := reflect.MakeSlice(val.Type(), len(list), len(list))
			for i, item := range list {
				if err := d.setReflectValue(newSlice.Index(i), item); err != nil {
					return err
				}
			}
			val.Set(newSlice)
		} else if str, ok := data.(string); ok && val.Type().Elem().Kind() == reflect.Uint8 {
			val.SetBytes([]byte(str))
		} else {
			return fmt.Errorf("cannot set slice with value of type %T", data)
		}

	case reflect.Map:
		if dict, ok := data.(map[string]any); ok {
			if val.IsNil() {
				val.Set(reflect.MakeMap(val.Type()))
			}

			for k, v := range dict {
				mapKey := reflect.New(val.Type().Key()).Elem()
				if err := d.setReflectValue(mapKey, k); err != nil {
					return err
				}

				mapVal := reflect.New(val.Type().Elem()).Elem()
				if err := d.setReflectValue(mapVal, v); err != nil {
					return err
				}

				val.SetMapIndex(mapKey, mapVal)
			}
		} else {
			return fmt.Errorf("cannot set map with value of type %T", data)
		}

	case reflect.Struct:
		if dict, ok := data.(map[string]any); ok {
			nestedDecoder := Decoder{rawBytes: d.rawBytes, curToken: d.curToken}
			return nestedDecoder.fillStruct(dict, val)
		} else {
			return fmt.Errorf("cannot set struct with value of type %T", data)
		}

	case reflect.Interface:
		if val.Type().NumMethod() == 0 {
			val.Set(reflect.ValueOf(data))
		} else {
			return fmt.Errorf("cannot set non-empty interface with value of type %T", data)
		}

	case reflect.Ptr:
		if val.IsNil() {
			val.Set(reflect.New(val.Type().Elem()))
		}
		return d.setReflectValue(val.Elem(), data)

	default:
		return fmt.Errorf("unsupported type: %v", val.Type())
	}

	return nil
}
