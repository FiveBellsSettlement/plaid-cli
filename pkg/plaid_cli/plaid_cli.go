package plaid_cli

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
)

type Data struct {
	DataDir     string
	Tokens      map[string]string
	Aliases     map[string]string
	BackAliases map[string]string
}

func LoadData(dataDir string) (*Data, error) {
	err := os.MkdirAll(filepath.Join(dataDir, "data"), os.ModePerm)
	if err != nil {
		return nil, err
	}

	data := &Data{
		DataDir:     dataDir,
		BackAliases: make(map[string]string),
	}

	data.loadTokens()
	data.loadAliases()

	return data, nil
}

func (d *Data) loadAliases() {
	aliases := make(map[string]string)
	filePath := d.aliasesPath()
	err := load(filePath, &aliases)
	if err != nil {
		log.Printf("Error loading aliases from %s. Assuming empty tokens.", d.aliasesPath())
	}

	d.Aliases = aliases

	for alias, itemID := range aliases {
		d.BackAliases[itemID] = alias
	}
}

func (d *Data) tokensPath() string {
	return filepath.Join(d.DataDir, "data", "tokens.json")
}

func (d *Data) aliasesPath() string {
	return filepath.Join(d.DataDir, "data", "aliases.json")
}

func (d *Data) loadTokens() {
	tokens := make(map[string]string)
	filePath := d.tokensPath()
	err := load(filePath, &tokens)
	if err != nil {
		log.Printf("Error loading tokens from %s. Assuming empty tokens.", d.tokensPath())
	}

	d.Tokens = tokens
}

func load(filePath string, v interface{}) (err error) {
	var f *os.File
	f, err = os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		err = errors.Join(err, closeErr)
	}()

	var b []byte
	b, err = io.ReadAll(f)
	if err != nil {
		return err
	}

	err = json.Unmarshal(b, v)
	return err
}

func (d *Data) Save() error {
	err := d.SaveTokens()
	if err != nil {
		return err
	}

	err = d.SaveAliases()
	if err != nil {
		return err
	}

	return nil
}

func (d *Data) SaveTokens() error {
	return save(d.Tokens, d.tokensPath())
}

func (d *Data) SaveAliases() error {
	return save(d.Aliases, d.aliasesPath())
}

func save(v interface{}, filePath string) (err error) {
	var f *os.File
	f, err = os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		err = errors.Join(err, closeErr)
	}()

	var b []byte
	b, err = json.Marshal(v)
	if err != nil {
		return err
	}

	_, err = f.Write(b)
	return err
}
