// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	_ "embed" // the public-good trusted root
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// publicGoodRoot is Sigstore's public-good trusted_root.json, fetched through
// its TUF repository by scripts/update-sigstore-root.sh. It changes only by
// that script and a reviewed commit, never at run time: a verifier that
// fetched its trust anchor while verifying would trust whatever the network
// said that day.
//
//go:embed sigstore_trusted_root.json
var publicGoodRoot []byte

// sigstoreRootMediaType is the one trusted-root document version this parser
// reads. A newer one is refused, not guessed at.
const sigstoreRootMediaType = "application/vnd.dev.sigstore.trustedroot+json;version=0.1"

// SigstoreRoot is the part of a Sigstore trusted root that verifying a keyless
// ledger entry needs: the Fulcio certificate authorities that may issue a
// signing certificate, and the Rekor logs that may witness a signature. The
// CT logs and timestamp authorities in the same document are not read,
// because the one bundle shape corral accepts relies on neither (keyless.go).
//
// It is ledger's own type, not sigstore-go's root.TrustedMaterial, because
// sigstore-go cannot be built for the browser (see Verify's keyless half).
type SigstoreRoot struct {
	cas  []fulcioCA
	logs map[string]rekorLog // keyed by hex(logId.keyId), as sigstore-go keys them
}

type fulcioCA struct {
	root          *x509.Certificate
	intermediates []*x509.Certificate
	start, end    time.Time // end zero: still valid
}

type rekorLog struct {
	key        *ecdsa.PublicKey // nil when the log's key is not ECDSA
	keyDetails string
	start, end time.Time // end zero: still valid
}

// covers reports whether [start, end] holds t; a zero end is open.
func covers(start, end, t time.Time) bool {
	return !t.Before(start) && (end.IsZero() || !t.After(end))
}

type rootValidFor struct {
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
}

type rootDoc struct {
	MediaType string `json:"mediaType"`
	Tlogs     []struct {
		PublicKey struct {
			RawBytes   []byte       `json:"rawBytes"`
			KeyDetails string       `json:"keyDetails"`
			ValidFor   rootValidFor `json:"validFor"`
		} `json:"publicKey"`
		LogID struct {
			KeyID []byte `json:"keyId"`
		} `json:"logId"`
	} `json:"tlogs"`
	CertificateAuthorities []struct {
		CertChain struct {
			Certificates []struct {
				RawBytes []byte `json:"rawBytes"`
			} `json:"certificates"`
		} `json:"certChain"`
		ValidFor rootValidFor `json:"validFor"`
	} `json:"certificateAuthorities"`
}

// ParseSigstoreRoot reads a Sigstore trusted_root.json. Every authority and
// log must carry a validity start: an authority with no stated start would be
// trusted for signatures made before it existed.
func ParseSigstoreRoot(js []byte) (*SigstoreRoot, error) {
	var doc rootDoc
	if err := json.NewDecoder(bytes.NewReader(js)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("sigstore root: %w", err)
	}
	if doc.MediaType != sigstoreRootMediaType {
		return nil, fmt.Errorf("sigstore root: media type %q, want %q", doc.MediaType, sigstoreRootMediaType)
	}
	if len(doc.CertificateAuthorities) == 0 || len(doc.Tlogs) == 0 {
		return nil, errors.New("sigstore root: names no certificate authority or no transparency log")
	}
	r := &SigstoreRoot{logs: map[string]rekorLog{}}
	for i, ca := range doc.CertificateAuthorities {
		certs := ca.CertChain.Certificates
		if len(certs) == 0 || ca.ValidFor.Start == nil {
			return nil, fmt.Errorf("sigstore root: certificate authority %d has no chain or no validity start", i)
		}
		f := fulcioCA{start: *ca.ValidFor.Start}
		if ca.ValidFor.End != nil {
			f.end = *ca.ValidFor.End
		}
		// The chain runs leaf-ward first; the last certificate is the root
		// (sigstore-go's ParseCertificateAuthority reads it the same way).
		for j, c := range certs {
			cert, err := x509.ParseCertificate(c.RawBytes)
			if err != nil {
				return nil, fmt.Errorf("sigstore root: certificate authority %d, certificate %d: %w", i, j, err)
			}
			if j == len(certs)-1 {
				f.root = cert
			} else {
				f.intermediates = append(f.intermediates, cert)
			}
		}
		r.cas = append(r.cas, f)
	}
	for i, tl := range doc.Tlogs {
		id := hex.EncodeToString(tl.LogID.KeyID)
		if len(tl.LogID.KeyID) == 0 || tl.PublicKey.ValidFor.Start == nil {
			return nil, fmt.Errorf("sigstore root: transparency log %d has no log id or no validity start", i)
		}
		if _, dup := r.logs[id]; dup {
			return nil, fmt.Errorf("sigstore root: transparency log %s is listed twice", id)
		}
		l := rekorLog{keyDetails: tl.PublicKey.KeyDetails, start: *tl.PublicKey.ValidFor.Start}
		if tl.PublicKey.ValidFor.End != nil {
			l.end = *tl.PublicKey.ValidFor.End
		}
		pub, err := x509.ParsePKIXPublicKey(tl.PublicKey.RawBytes)
		if err != nil {
			return nil, fmt.Errorf("sigstore root: transparency log %s: %w", id, err)
		}
		// A log with another key type (Rekor v2's ed25519 logs) is kept, so
		// an entry naming it fails saying so rather than "unknown log".
		l.key, _ = pub.(*ecdsa.PublicKey)
		r.logs[id] = l
	}
	return r, nil
}

// PublicGoodTrustedRoot is Sigstore's public-good root as embedded in this
// build. Callers pass it explicitly as Trust.SigstoreRoot: there is no
// default root.
func PublicGoodTrustedRoot() (*SigstoreRoot, error) {
	return ParseSigstoreRoot(publicGoodRoot)
}

// CertificateAuthorities is how many Fulcio authorities the root holds.
func (r *SigstoreRoot) CertificateAuthorities() int { return len(r.cas) }

// HasLog reports whether the root holds the transparency log with this hex id.
func (r *SigstoreRoot) HasLog(hexID string) bool { _, ok := r.logs[hexID]; return ok }
