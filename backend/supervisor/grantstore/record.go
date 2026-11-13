// Package grantstore は1承認1slotの消費・失効記録を扱う未配線の境界。
// checksum は破損検出だけで、真正性・ACL・削除/rollback 防止ではない。
package grantstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var ErrInvalidRecord = errors.New("grant 記録を受理できません")

type Phase byte

const (
	Ready Phase = iota + 1
	Intent
	Consumed
	Revoked
)

const RecordSize = 89

type Record struct {
	Phase                Phase
	Revision, Generation uint64
	GrantDigest          [32]byte
}

func fingerprint(g supervisor.ProvisionedGrant) ([32]byte, error) {
	if g.Binding.Generation == 0 || !g.Binding.Identity.Valid() || !utf8.ValidString(g.Binding.Identity.LaunchID) || !utf8.ValidString(g.Binding.Identity.Epoch) {
		return [32]byte{}, ErrInvalidRecord
	}
	canonical := struct {
		Generation          uint64
		Identity            supervisor.Identity
		KeySetDigest        [32]byte
		GuestBoundary       supervisor.GuestBoundary
		HardDeadline        time.Time
		BackendLeaseCeiling time.Duration
	}{g.Binding.Generation, g.Binding.Identity, g.KeySetDigest, g.GuestBoundary, g.HardDeadline.Round(0).UTC(), g.BackendLeaseCeiling}
	data, err := json.Marshal(canonical)
	if err != nil {
		return [32]byte{}, ErrInvalidRecord
	}
	return sha256.Sum256(append([]byte("rescue-host-grant/1\x00"), data...)), nil
}

// InitialRecord は型付き承認値の結合を計算するだけ。新規承認/世代発行ではない。
// 保護 provisioning だけが未使用slotを事前作成でき、真正性と非再利用を担う。
func InitialRecord(g supervisor.ProvisionedGrant) (Record, error) {
	digest, err := fingerprint(g)
	if err != nil {
		return Record{}, err
	}
	return Record{Ready, 1, g.Binding.Generation, digest}, nil
}

func validRecord(r Record) bool {
	return r.Phase >= Ready && r.Phase <= Revoked && r.Revision == uint64(r.Phase) && r.Generation != 0 && r.GrantDigest != ([32]byte{})
}

func EncodeRecord(r Record) ([]byte, error) {
	if !validRecord(r) {
		return nil, ErrInvalidRecord
	}
	data := make([]byte, RecordSize)
	copy(data, "RHGRANT1")
	data[8] = byte(r.Phase)
	binary.BigEndian.PutUint64(data[9:17], r.Revision)
	binary.BigEndian.PutUint64(data[17:25], r.Generation)
	copy(data[25:57], r.GrantDigest[:])
	sum := sha256.Sum256(data[:57])
	copy(data[57:], sum[:])
	return data, nil
}

func DecodeRecord(data []byte) (Record, error) {
	if len(data) != RecordSize || string(data[:8]) != "RHGRANT1" {
		return Record{}, ErrInvalidRecord
	}
	sum := sha256.Sum256(data[:57])
	if !bytes.Equal(data[57:], sum[:]) {
		return Record{}, ErrInvalidRecord
	}
	r := Record{Phase: Phase(data[8]), Revision: binary.BigEndian.Uint64(data[9:17]), Generation: binary.BigEndian.Uint64(data[17:25])}
	copy(r.GrantDigest[:], data[25:57])
	if !validRecord(r) {
		return Record{}, ErrInvalidRecord
	}
	return r, nil
}
