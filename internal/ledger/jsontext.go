// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"encoding/json"
	"reflect"
)

// JSONText is a recorded field stored as JSON text whose decoded shape is T.
//
// It is a string underneath, so a ScanRow still marshals the field as one
// JSON string and every signed entry written before it parses and verifies
// unchanged. The type parameter exists so the reflective tier gate
// (TestEveryRecordedFieldHasATier) can see inside the text: it asks
// EncodedType and walks that type's fields, which a bare string would hide.
type JSONText[T any] string

// EncodedType is the type the text decodes to, for the tier gate's walk.
func (JSONText[T]) EncodedType() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// EncodeJSONText renders v as JSON text. A nil or empty slice or map is ""
// (nothing was recorded), never "null" or "[]".
func EncodeJSONText[T any](v T) (JSONText[T], error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map:
		if rv.Len() == 0 {
			return "", nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return JSONText[T](b), nil
}

// Decode parses the text. "" is the zero T and no error.
func (j JSONText[T]) Decode() (T, error) {
	var v T
	if j == "" {
		return v, nil
	}
	err := json.Unmarshal([]byte(j), &v)
	return v, err
}
