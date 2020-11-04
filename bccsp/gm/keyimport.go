/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package gm

import (
	"crypto/x509"
	"errors"
	"fmt"
	"reflect"

	"github.com/paul-lee-attorney/fabric-2.1-gm/bccsp"
	"github.com/paul-lee-attorney/gm/sm2"
)

type sm4ImportKeyOptsKeyImporter struct{}

func (*sm4ImportKeyOptsKeyImporter) KeyImport(raw interface{}, opts bccsp.KeyImportOpts) (bccsp.Key, error) {
	sm4Raw, ok := raw.([]byte)
	if !ok {
		return nil, errors.New("invalid raw material, expected byte array")
	}

	if sm4Raw == nil {
		return nil, errors.New("invalid raw material, it must not be nil")
	}

	if len(sm4Raw) != 16 {
		return nil, fmt.Errorf("invalid Key Length [%d], must be 16 bytes", len(sm4Raw))
	}

	return &sm4PrivateKey{sm4Raw, false}, nil
}

type sm2PKIXPublicKeyImportOptsKeyImporter struct{}

func (*sm2PKIXPublicKeyImportOptsKeyImporter) KeyImport(raw interface{}, opts bccsp.KeyImportOpts) (bccsp.Key, error) {
	der, ok := raw.([]byte)
	if !ok {
		return nil, errors.New("Invalid raw material. Expected byte array.")
	}

	if len(der) == 0 {
		return nil, errors.New("Invalid raw. It must not be nil.")
	}

	sm2PK, err := parsePKIXSM2PublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("Failed converting PKIX to SM2 public key [%s]", err)
	}

	return &sm2PublicKey{sm2PK}, nil
}

type sm2PrivateKeyImportOptsKeyImporter struct{}

func (*sm2PrivateKeyImportOptsKeyImporter) KeyImport(raw interface{}, opts bccsp.KeyImportOpts) (bccsp.Key, error) {
	der, ok := raw.([]byte)
	if !ok {
		return nil, errors.New("Invalid raw material for SM2 private key import,expected byte array")
	}

	if len(der) == 0 {
		return nil, errors.New("invalid raw, it must not be nil")
	}

	sm2Priv, err := parsePKCS8SM2PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("Failed converting PKIX to SM2 public key [%s]", err)
	}

	return &sm2PrivateKey{sm2Priv}, nil
}

type sm2GoPublicKeyImportOptsKeyImporter struct{}

func (*sm2GoPublicKeyImportOptsKeyImporter) KeyImport(raw interface{}, opts bccsp.KeyImportOpts) (bccsp.Key, error) {
	pubKey, ok := raw.(*sm2.PublicKey)
	if !ok {
		return nil, errors.New("invalid raw material. Expected *sm2.PublicKey")
	}

	return &sm2PublicKey{pubKey}, nil
}

type x509PublicKeyImportOptsKeyImporter struct {
	bccsp *CSP
}

func (ki *x509PublicKeyImportOptsKeyImporter) KeyImport(raw interface{}, opts bccsp.KeyImportOpts) (bccsp.Key, error) {
	x509Cert, ok := raw.(*x509.Certificate)
	if !ok {
		return nil, errors.New("Invalid raw material. Expected *x509.Certificate.")
	}

	pk := x509Cert.PublicKey

	switch pk.(type) {
	case *sm2.PublicKey:
		return ki.bccsp.KeyImporters[reflect.TypeOf(&bccsp.SM2GoPublicKeyImportOpts{})].KeyImport(
			pk,
			&bccsp.SM2GoPublicKeyImportOpts{Temporary: opts.Ephemeral()})
	default:
		return nil, errors.New("Certificate's public key type not recognized. Supported keys: [ECDSA].")
	}
}