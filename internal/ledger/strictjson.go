// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// strictjson is the shape gate in front of every JSON document the keyless
// verifier reads (the bundle and the Rekor body). encoding/json alone is not
// strict: it matches keys case-insensitively ("MEDIATYPE" fills MediaType),
// lets a duplicate key's LAST value win (so a decoy messageSignature placed
// first is silently replaced, or the reverse), and DisallowUnknownFields
// does not see either. Two readers of one document that disagree about what
// it says is how a signed object gets reinterpreted, so before decoding,
// the document is walked token by token: every key must be an exact-case
// member of the set allowed at that position, no object may repeat a key,
// every value must be the kind (object, array, scalar) the shape expects,
// and nothing may follow the top-level value.

type jsonKind int

const (
	jScalar jsonKind = iota // a string, number, bool or null
	jObject
	jArray
)

// jsonShape is what one value may be. fields (jObject) maps each allowed key
// to its value's shape; elem (jArray) is every element's shape.
type jsonShape struct {
	kind   jsonKind
	fields map[string]*jsonShape
	elem   *jsonShape
}

var jsonLeaf = &jsonShape{kind: jScalar}

func jsonObj(fields map[string]*jsonShape) *jsonShape {
	return &jsonShape{kind: jObject, fields: fields}
}

// bundleShape is the one Sigstore bundle shape accepted (see keyless.go).
var bundleShape = jsonObj(map[string]*jsonShape{
	"mediaType": jsonLeaf,
	"verificationMaterial": jsonObj(map[string]*jsonShape{
		"certificate": jsonObj(map[string]*jsonShape{"rawBytes": jsonLeaf}),
		"tlogEntries": {kind: jArray, elem: jsonObj(map[string]*jsonShape{
			"logIndex":         jsonLeaf,
			"logId":            jsonObj(map[string]*jsonShape{"keyId": jsonLeaf}),
			"kindVersion":      jsonObj(map[string]*jsonShape{"kind": jsonLeaf, "version": jsonLeaf}),
			"integratedTime":   jsonLeaf,
			"inclusionPromise": jsonObj(map[string]*jsonShape{"signedEntryTimestamp": jsonLeaf}),
			"inclusionProof": jsonObj(map[string]*jsonShape{
				"logIndex":   jsonLeaf,
				"rootHash":   jsonLeaf,
				"treeSize":   jsonLeaf,
				"hashes":     {kind: jArray, elem: jsonLeaf},
				"checkpoint": jsonObj(map[string]*jsonShape{"envelope": jsonLeaf}),
			}),
			"canonicalizedBody": jsonLeaf,
		})},
	}),
	"messageSignature": jsonObj(map[string]*jsonShape{
		"messageDigest": jsonObj(map[string]*jsonShape{"algorithm": jsonLeaf, "digest": jsonLeaf}),
		"signature":     jsonLeaf,
	}),
})

// hashedRekordShape is a Rekor v1 hashedrekord v0.0.1 body.
var hashedRekordShape = jsonObj(map[string]*jsonShape{
	"apiVersion": jsonLeaf,
	"kind":       jsonLeaf,
	"spec": jsonObj(map[string]*jsonShape{
		"data": jsonObj(map[string]*jsonShape{
			"hash": jsonObj(map[string]*jsonShape{"algorithm": jsonLeaf, "value": jsonLeaf}),
		}),
		"signature": jsonObj(map[string]*jsonShape{
			"content":   jsonLeaf,
			"publicKey": jsonObj(map[string]*jsonShape{"content": jsonLeaf}),
		}),
	}),
})

// strictDecode checks b against shape, then decodes it into v. Once the walk
// passed, encoding/json's leniencies have nothing left to act on.
func strictDecode(b []byte, shape *jsonShape, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := walkJSON(dec, shape, "$"); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return json.Unmarshal(b, v)
}

func walkJSON(dec *json.Decoder, s *jsonShape, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	d, isDelim := tok.(json.Delim)
	switch {
	case s.kind == jScalar && isDelim:
		return fmt.Errorf("%s: want a scalar, got %v", path, d)
	case s.kind == jScalar:
		return nil
	case s.kind == jObject && d != '{':
		return fmt.Errorf("%s: want an object", path)
	case s.kind == jArray && d != '[':
		return fmt.Errorf("%s: want an array", path)
	case s.kind == jArray:
		for i := 0; dec.More(); i++ {
			if err := walkJSON(dec, s.elem, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		_, err := dec.Token() // ']'
		return err
	}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		k, _ := kt.(string) // an object key is always a string token
		if seen[k] {
			return fmt.Errorf("%s: duplicate field %q", path, k)
		}
		seen[k] = true
		sub, ok := s.fields[k]
		if !ok {
			return fmt.Errorf("%s: unknown field %q", path, k)
		}
		if err := walkJSON(dec, sub, path+"."+k); err != nil {
			return err
		}
	}
	_, err = dec.Token() // '}'
	return err
}
