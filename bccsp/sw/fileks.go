/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sw

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/paul-lee-attorney/fabric-2.1-gm/bccsp"
	"github.com/paul-lee-attorney/fabric-2.1-gm/bccsp/utils"
	"github.com/paul-lee-attorney/gm/sm2"
)

// NewFileBasedKeyStore instantiated a file-based key store at a given position.
// The key store can be encrypted if a non-empty password is specified.
// It can be also be set as read only. In this case, any store operation
// will be forbidden
func NewFileBasedKeyStore(pwd []byte, path string, readOnly bool) (bccsp.KeyStore, error) {
	ks := &fileBasedKeyStore{}
	return ks, ks.Init(pwd, path, readOnly)
}

// fileBasedKeyStore is a folder-based KeyStore.
// Each key is stored in a separated file whose name contains the key's SKI
// and flags to identity the key's type. All the keys are stored in
// a folder whose path is provided at initialization time.
// The KeyStore can be initialized with a password, this password
// is used to encrypt and decrypt the files storing the keys.
// A KeyStore can be read only to avoid the overwriting of keys.
type fileBasedKeyStore struct {
	path string

	readOnly bool
	isOpen   bool

	pwd []byte

	// Sync
	m sync.Mutex
}

// Init initializes this KeyStore with a password, a path to a folder
// where the keys are stored and a read only flag.
// Each key is stored in a separated file whose name contains the key's SKI
// and flags to identity the key's type.
// If the KeyStore is initialized with a password, this password
// is used to encrypt and decrypt the files storing the keys.
// The pwd can be nil for non-encrypted KeyStores. If an encrypted
// key-store is initialized without a password, then retrieving keys from the
// KeyStore will fail.
// A KeyStore can be read only to avoid the overwriting of keys.
func (ks *fileBasedKeyStore) Init(pwd []byte, path string, readOnly bool) error {
	// Validate inputs
	// pwd can be nil

	if len(path) == 0 {
		return errors.New("an invalid KeyStore path provided. Path cannot be an empty string")
	}

	ks.m.Lock()
	defer ks.m.Unlock()

	if ks.isOpen {
		return errors.New("keystore is already initialized")
	}

	ks.path = path

	clone := make([]byte, len(pwd))
	copy(clone, pwd) // revised ks.pwd into clone, wich might be a typo
	ks.pwd = clone
	ks.readOnly = readOnly

	exists, err := dirExists(path)
	if err != nil {
		return err
	}
	if !exists {
		err = ks.createKeyStore()
		if err != nil {
			return err
		}
		return ks.openKeyStore()
	}

	empty, err := dirEmpty(path)
	if err != nil {
		return err
	}
	if empty {
		err = ks.createKeyStore()
		if err != nil {
			return err
		}
	}

	return ks.openKeyStore()
}

// ReadOnly returns true if this KeyStore is read only, false otherwise.
// If ReadOnly is true then StoreKey will fail.
func (ks *fileBasedKeyStore) ReadOnly() bool {
	return ks.readOnly
}

// GetKey returns a key object whose SKI is the one passed.
func (ks *fileBasedKeyStore) GetKey(ski []byte) (bccsp.Key, error) {
	// Validate arguments
	if len(ski) == 0 {
		return nil, errors.New("invalid SKI. Cannot be of zero length")
	}

	// 将SKI编码转换为ASCII编码并获取尾缀
	suffix := ks.getSuffix(hex.EncodeToString(ski))

	switch suffix {
	case "key": // 对称密码算法的秘钥
		// Load the key
		// 载入对称密码算法的秘钥，就PEM消息加密解密算法进行SM4改造
		key, err := ks.loadKey(hex.EncodeToString(ski))
		if err != nil {
			return nil, fmt.Errorf("failed loading key [%x] [%s]", ski, err)
		}
		return &aesPrivateKey{key, false}, nil
	case "sm4key":
		key, err := ks.loadSM4Key(hex.EncodeToString(ski))
		if err != nil {
			return nil, fmt.Errorf("failed loading sm4key [%x] [%s]", ski, err)
		}
		return &sm4PrivateKey{key, false}, nil
	case "sk":
		// Load the private key
		// 载入不对称算法的私钥
		key, err := ks.loadPrivateKey(hex.EncodeToString(ski))
		if err != nil {
			return nil, fmt.Errorf("failed loading secret key [%x] [%s]", ski, err)
		}

		switch k := key.(type) {
		case *ecdsa.PrivateKey:
			return &ecdsaPrivateKey{k}, nil
		case *sm2.PrivateKey: // private key of sm2
			return &sm2PrivateKey{k}, nil
		default:
			return nil, errors.New("secret key type not recognized")
		}
	case "pk":
		// Load the public key
		// 载入不对称算法的公钥
		key, err := ks.loadPublicKey(hex.EncodeToString(ski))
		if err != nil {
			return nil, fmt.Errorf("failed loading public key [%x] [%s]", ski, err)
		}

		switch k := key.(type) {
		case *ecdsa.PublicKey:
			return &ecdsaPublicKey{k}, nil
		case *sm2.PublicKey: // public key of sm2
			return &sm2PublicKey{k}, nil
		default:
			return nil, errors.New("public key type not recognized")
		}
	default:
		return ks.searchKeystoreForSKI(ski)
	}
}

// StoreKey stores the key k in this KeyStore.
// If this KeyStore is read only then the method will fail.
func (ks *fileBasedKeyStore) StoreKey(k bccsp.Key) (err error) {
	if ks.readOnly {
		return errors.New("read only KeyStore")
	}

	if k == nil {
		return errors.New("invalid key. It must be different from nil")
	}
	switch kk := k.(type) {
	case *ecdsaPrivateKey:
		err = ks.storePrivateKey(hex.EncodeToString(k.SKI()), kk.privKey)
		if err != nil {
			return fmt.Errorf("failed storing ECDSA private key [%s]", err)
		}
	case *sm2PrivateKey:
		err = ks.storePrivateKey(hex.EncodeToString(k.SKI()), kk.privKey)
		if err != nil {
			return fmt.Errorf("failed storing SM2 private key [%s]", err)
		}

	case *ecdsaPublicKey:
		err = ks.storePublicKey(hex.EncodeToString(k.SKI()), kk.pubKey)
		if err != nil {
			return fmt.Errorf("failed storing ECDSA public key [%s]", err)
		}

	case *sm2PublicKey:
		err = ks.storePublicKey(hex.EncodeToString(k.SKI()), kk.pubKey)
		if err != nil {
			return fmt.Errorf("failed storing SM2 public key [%s]", err)
		}

	case *aesPrivateKey:
		err = ks.storeKey(hex.EncodeToString(k.SKI()), kk.privKey)
		if err != nil {
			return fmt.Errorf("failed storing AES key [%s]", err)
		}

	case *sm4PrivateKey:
		err = ks.storeSm4Key(hex.EncodeToString(k.SKI()), kk.privKey)
		if err != nil {
			return fmt.Errorf("failed storing SM4 key [%s]", err)
		}

	default:
		return fmt.Errorf("key type not reconigned [%s]", k)
	}

	return
}

func (ks *fileBasedKeyStore) searchKeystoreForSKI(ski []byte) (k bccsp.Key, err error) {

	files, _ := ioutil.ReadDir(ks.path)
	for _, f := range files {
		if f.IsDir() {
			continue
		}

		if f.Size() > (1 << 16) { //64k, somewhat arbitrary limit, considering even large keys
			continue
		}

		raw, err := ioutil.ReadFile(filepath.Join(ks.path, f.Name()))
		if err != nil {
			continue
		}

		key, err := utils.PEMtoPrivateKey(raw, ks.pwd)
		if err != nil {
			continue
		}

		switch kk := key.(type) {
		case *ecdsa.PrivateKey:
			k = &ecdsaPrivateKey{kk}
		case *sm2.PrivateKey: // SM2 private key
			k = &sm2PrivateKey{kk}
		default:
			continue
		}

		if !bytes.Equal(k.SKI(), ski) {
			continue
		}

		return k, nil
	}
	return nil, fmt.Errorf("key with SKI %x not found in %s", ski, ks.path)
}

func (ks *fileBasedKeyStore) getSuffix(alias string) string {
	files, _ := ioutil.ReadDir(ks.path)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), alias) {
			if strings.HasSuffix(f.Name(), "sk") {
				return "sk"
			}
			if strings.HasSuffix(f.Name(), "pk") {
				return "pk"
			}
			if strings.HasSuffix(f.Name(), "key") {
				return "key"
			}
			break
		}
	}
	return ""
}

func (ks *fileBasedKeyStore) storePrivateKey(alias string, privateKey interface{}) error {
	rawKey, err := utils.PrivateKeyToPEM(privateKey, ks.pwd)
	if err != nil {
		logger.Errorf("Failed converting private key to PEM [%s]: [%s]", alias, err)
		return err
	}

	err = ioutil.WriteFile(ks.getPathForAlias(alias, "sk"), rawKey, 0600) //user has read and write authority
	if err != nil {
		logger.Errorf("Failed storing private key [%s]: [%s]", alias, err)
		return err
	}

	return nil
}

func (ks *fileBasedKeyStore) storePublicKey(alias string, publicKey interface{}) error {
	rawKey, err := utils.PublicKeyToPEM(publicKey, ks.pwd)
	if err != nil {
		logger.Errorf("Failed converting public key to PEM [%s]: [%s]", alias, err)
		return err
	}

	err = ioutil.WriteFile(ks.getPathForAlias(alias, "pk"), rawKey, 0600)
	if err != nil {
		logger.Errorf("Failed storing private key [%s]: [%s]", alias, err)
		return err
	}

	return nil
}

func (ks *fileBasedKeyStore) storeKey(alias string, key []byte) error {
	pem, err := utils.AEStoEncryptedPEM(key, ks.pwd)
	if err != nil {
		logger.Errorf("Failed converting key to PEM [%s]: [%s]", alias, err)
		return err
	}

	err = ioutil.WriteFile(ks.getPathForAlias(alias, "key"), pem, 0600)
	if err != nil {
		logger.Errorf("Failed storing key [%s]: [%s]", alias, err)
		return err
	}

	return nil
}

func (ks *fileBasedKeyStore) storeSm4Key(alias string, key []byte) error {

	blockType := "SM4 PRIVATE KEY"

	pem, err := utils.SM4EncryptPEMBlock(blockType, key, ks.pwd)

	if err != nil {
		logger.Errorf("Failed converting key to PEM [%s]: [%s]", alias, err)
		return err
	}

	err = ioutil.WriteFile(ks.getPathForAlias(alias, "sm4key"), pem, 0600)
	if err != nil {
		logger.Errorf("Failed storing key [%s]: [%s]", alias, err)
		return err
	}

	return nil
}

func (ks *fileBasedKeyStore) loadPrivateKey(alias string) (interface{}, error) {
	path := ks.getPathForAlias(alias, "sk")
	logger.Debugf("Loading private key [%s] at [%s]...", alias, path)

	raw, err := ioutil.ReadFile(path)
	if err != nil {
		logger.Errorf("Failed loading private key [%s]: [%s].", alias, err.Error())

		return nil, err
	}

	privateKey, err := utils.PEMtoPrivateKey(raw, ks.pwd)
	if err != nil {
		logger.Errorf("Failed parsing private key [%s]: [%s].", alias, err.Error())

		return nil, err
	}

	return privateKey, nil
}

func (ks *fileBasedKeyStore) loadPublicKey(alias string) (interface{}, error) {
	path := ks.getPathForAlias(alias, "pk")
	logger.Debugf("Loading public key [%s] at [%s]...", alias, path)

	raw, err := ioutil.ReadFile(path)
	if err != nil {
		logger.Errorf("Failed loading public key [%s]: [%s].", alias, err.Error())

		return nil, err
	}

	privateKey, err := utils.PEMtoPublicKey(raw, ks.pwd)
	if err != nil {
		logger.Errorf("Failed parsing private key [%s]: [%s].", alias, err.Error())

		return nil, err
	}

	return privateKey, nil
}

func (ks *fileBasedKeyStore) loadKey(alias string) ([]byte, error) {
	path := ks.getPathForAlias(alias, "key")
	logger.Debugf("Loading key [%s] at [%s]...", alias, path)

	pem, err := ioutil.ReadFile(path)
	if err != nil {
		logger.Errorf("Failed loading key [%s]: [%s].", alias, err.Error())
		return nil, err
	}

	key, err := utils.PEMtoAES(pem, ks.pwd)
	if err != nil {
		logger.Errorf("Failed parsing key [%s]: [%s]", alias, err)
		return nil, err
	}
	return key, nil
}

func (ks *fileBasedKeyStore) loadSM4Key(alias string) ([]byte, error) {
	path := ks.getPathForAlias(alias, "sm4key")
	logger.Debugf("Loading key [%s] at [%s]...", alias, path)

	pem, err := ioutil.ReadFile(path)
	if err != nil {
		logger.Errorf("Failed loading key [%s]: [%s].", alias, err.Error())
		return nil, err
	}

	key, err := utils.PEMtoAES(pem, ks.pwd)
	if err != nil {
		logger.Errorf("Failed parsing key [%s]: [%s]", alias, err)
		return nil, err
	}
	return key, nil
}

func (ks *fileBasedKeyStore) createKeyStore() error {
	// Create keystore directory root if it doesn't exist yet
	ksPath := ks.path
	logger.Debugf("Creating KeyStore at [%s]...", ksPath)

	err := os.MkdirAll(ksPath, 0755)
	if err != nil {
		return err
	}

	logger.Debugf("KeyStore created at [%s].", ksPath)
	return nil
}

func (ks *fileBasedKeyStore) openKeyStore() error {
	if ks.isOpen {
		return nil
	}
	ks.isOpen = true
	logger.Debugf("KeyStore opened at [%s]...done", ks.path)

	return nil
}

func (ks *fileBasedKeyStore) getPathForAlias(alias, suffix string) string {
	return filepath.Join(ks.path, alias+"_"+suffix)
}

func dirExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func dirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}
