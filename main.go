package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/landakram/plaid-cli/pkg/plaid_cli"
	"github.com/manifoldco/promptui"
	"github.com/plaid/plaid-go/v26/plaid"
	"github.com/spf13/cobra"

	"github.com/spf13/viper"

	"github.com/Xuanwo/go-locale"
	"golang.org/x/text/language"
)

func sliceToMap(slice []string) map[string]bool {
	set := make(map[string]bool, len(slice))
	for _, s := range slice {
		set[s] = true
	}
	return set
}

var plaidSupportedLanguages = []string{"en", "fr", "es", "nl"}

func IsValidLanguageCode(lang string) bool {
	supportedLanguages := sliceToMap(plaidSupportedLanguages)
	return supportedLanguages[lang]
}

func main() {
	log.SetFlags(0)

	usr, _ := user.Current()
	dir := usr.HomeDir
	viper.SetDefault("cli.data_dir", filepath.Join(dir, ".plaid-cli"))

	dataDir := viper.GetString("cli.data_dir")
	data, err := plaid_cli.LoadData(dataDir)

	if err != nil {
		log.Fatal(err)
	}

	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath(dataDir)
	viper.AddConfigPath(".")
	allowedErr := viper.ConfigFileNotFoundError{}
	err = viper.ReadInConfig()
	if err != nil && !errors.Is(err, allowedErr) {
		log.Fatal(err)
	}

	viper.SetEnvPrefix("")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	tag, err := locale.Detect()
	if err != nil {
		tag = language.AmericanEnglish
	}

	region, _ := tag.Region()
	base, _ := tag.Base()

	var country string
	if region.IsCountry() {
		country = region.String()
	} else {
		country = "US"
	}

	lang := base.String()

	viper.SetDefault("plaid.countries", []string{country})
	countriesOpt := viper.GetStringSlice("plaid.countries")
	var countries []plaid.CountryCode
	for _, c := range countriesOpt {
		uc := strings.ToUpper(c)
		cc, err := plaid.NewCountryCodeFromValue(uc)
		if err != nil {
			log.Fatalf("⚠️  Invalid country code %s. Please configure `plaid.countries` (using an envvar, PLAID_COUNTRIES, or in plaid-cli's config file) to a subset of countries that Plaid supports: %v\n", c, err)
		}
		countries = append(countries, *cc)
	}

	viper.SetDefault("plaid.language", lang)
	lang = viper.GetString("plaid.language")

	if !IsValidLanguageCode(lang) {
		log.Fatalln("⚠️  Invalid language code. Please configure `plaid.language` (using an envvar, PLAID_LANGUAGE, or in plaid-cli's config file) to a language that Plaid supports. Plaid supports the following languages: ", plaidSupportedLanguages)
	}

	viper.SetDefault("plaid.environment", "development")
	plaidEnvStr := strings.ToLower(viper.GetString("plaid.environment"))

	var plaidEnv plaid.Environment
	switch plaidEnvStr {
	case "development":
		plaidEnv = plaid.Development
	case "production":
		plaidEnv = plaid.Production
	default:
		log.Fatalln("Invalid plaid environment. Valid plaid environments are 'development' or 'production'.")
	}

	clientId := viper.GetString("plaid.client_id")
	secret := viper.GetString("plaid.secret")

	conf := plaid.NewConfiguration()
	conf.AddDefaultHeader("PLAID-CLIENT-ID", clientId)
	conf.AddDefaultHeader("PLAID-SECRET", secret)
	conf.UseEnvironment(plaidEnv)
	client := plaid.NewAPIClient(conf).PlaidApi

	if err != nil {
		log.Fatal(err)
	}

	linker := plaid_cli.NewLinker(data, client, countries, lang)

	linkCommand := &cobra.Command{
		Use:   "link [ITEM-ID-OR-ALIAS]",
		Short: "Link an institution so plaid-cli can pull transactions",
		Long:  "Link an institution so plaid-cli can pull transactions. An item ID or alias can be passed to initiate a relink.",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			port := viper.GetString("link.port")

			var tokenPair *plaid_cli.TokenPair

			var err error

			if len(args) > 0 && len(args[0]) > 0 {
				itemOrAlias := args[0]

				itemID, ok := data.Aliases[itemOrAlias]
				if ok {
					itemOrAlias = itemID
				}

				err = linker.Relink(itemOrAlias, port)
				if err == nil {
					log.Println("Institution relinked!")
				}
			} else {
				tokenPair, err = linker.Link(port)
				if err != nil {
					log.Fatalln(err)
				}
				data.Tokens[tokenPair.ItemID] = tokenPair.AccessToken
				err = data.Save()
			}

			if err != nil {
				log.Fatalln(err)
			}

			log.Println("Institution linked!")
			log.Printf("Item ID: %s\n", tokenPair.ItemID)

			if alias, ok := data.BackAliases[tokenPair.ItemID]; ok {
				log.Printf("Alias: %s\n", alias)
				return
			}

			validate := func(input string) error {
				matched, err := regexp.Match(`^\w+$`, []byte(input))
				if err != nil {
					return err
				}

				if !matched && input != "" {
					return errors.New("valid characters: [0-9A-Za-z_]")
				}

				return nil
			}

			log.Println("You can give the institution a friendly alias and use that instead of the item ID in most commands.")
			prompt := promptui.Prompt{
				Label:    "Alias (default: none)",
				Validate: validate,
			}

			input, err := prompt.Run()
			if err != nil {
				log.Fatalln(err)
			}

			if input != "" {
				err = SetAlias(data, tokenPair.ItemID, input)
				if err != nil {
					log.Fatalln(err)
				}
			}
		},
	}

	linkCommand.Flags().StringP("port", "p", "8080", "Port on which to serve Plaid Link")
	err = viper.BindPFlag("link.port", linkCommand.Flags().Lookup("port"))
	if err != nil {
		log.Fatalln(err)
	}

	tokensCommand := &cobra.Command{
		Use:   "tokens",
		Short: "List access tokens",
		Run: func(cmd *cobra.Command, args []string) {
			resolved := make(map[string]string, len(data.Tokens))
			for itemID, token := range data.Tokens {
				if alias, ok := data.BackAliases[itemID]; ok {
					resolved[alias] = token
				} else {
					resolved[itemID] = token
				}
			}

			printJSON, err := json.MarshalIndent(resolved, "", "  ")
			if err != nil {
				log.Fatalln(err)
			}
			fmt.Println(string(printJSON))
		},
	}

	aliasCommand := &cobra.Command{
		Use:   "alias [ITEM-ID] [NAME]",
		Short: "Give a linked institution a friendly name",
		Long:  "Give a linked institution a friendly name. You can use this name instead of the idem ID in most commands.",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			itemID := args[0]
			alias := args[1]

			err := SetAlias(data, itemID, alias)
			if err != nil {
				log.Fatalln(err)
			}
		},
	}

	aliasesCommand := &cobra.Command{
		Use:   "aliases",
		Short: "List aliases",
		Run: func(cmd *cobra.Command, args []string) {
			printJSON, err := json.MarshalIndent(data.Aliases, "", "  ")
			if err != nil {
				log.Fatalln(err)
			}
			fmt.Println(string(printJSON))
		},
	}

	accountsCommand := &cobra.Command{
		Use:   "accounts [ITEM-ID-OR-ALIAS]",
		Short: "List accounts for a given institution",
		Long:  "List accounts for a given institution. An account ID returned from this command can be used as a filter when listing transactions.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			itemOrAlias := args[0]
			itemID, ok := data.Aliases[itemOrAlias]
			if ok {
				itemOrAlias = itemID
			}

			err := WithRelinkOnAuthError(itemOrAlias, linker, func() error {
				token := data.Tokens[itemOrAlias]
				ctx := context.Background()
				req := plaid.NewAccountsGetRequest(token)
				apiReq := client.AccountsGet(ctx)
				apiReq = apiReq.AccountsGetRequest(*req)
				res, _, err := apiReq.Execute()
				if err != nil {
					return err
				}

				b, err := json.MarshalIndent(res.Accounts, "", "  ")
				if err != nil {
					return err
				}

				fmt.Println(string(b))

				return nil
			})

			if err != nil {
				log.Fatalln(err)
			}
		},
	}

	var fromFlag string
	var toFlag string
	var accountID string
	var outputFormat string
	transactionsCommand := &cobra.Command{
		Use:   "transactions [ITEM-ID-OR-ALIAS]",
		Short: "List transactions for a given institution",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			itemOrAlias := args[0]
			itemID, ok := data.Aliases[itemOrAlias]
			if ok {
				itemOrAlias = itemID
			}

			err := WithRelinkOnAuthError(itemOrAlias, linker, func() error {
				token := data.Tokens[itemOrAlias]

				var accountIDs []string
				if len(accountID) > 0 {
					accountIDs = append(accountIDs, accountID)
				}
				count := int32(100)
				offset := int32(0)

				req := plaid.NewTransactionsGetRequest(token, fromFlag, toFlag)
				req.SetOptions(plaid.TransactionsGetRequestOptions{
					AccountIds: &accountIDs,
					Count:      &count,
					Offset:     &offset,
				})

				transactions, err := AllTransactions(*req, client)
				if err != nil {
					return err
				}

				serializer, err := NewTransactionSerializer(outputFormat)
				if err != nil {
					return err
				}

				b, err := serializer.serialize(transactions)
				if err != nil {
					return err
				}

				fmt.Println(string(b))

				return nil
			})

			if err != nil {
				log.Fatalln(err)
			}
		},
	}
	transactionsCommand.Flags().StringVarP(&fromFlag, "from", "f", "", "Date of first transaction (required)")
	err = transactionsCommand.MarkFlagRequired("from")
	if err != nil {
		log.Fatalln(err)
	}

	transactionsCommand.Flags().StringVarP(&toFlag, "to", "t", "", "Date of last transaction (required)")
	err = transactionsCommand.MarkFlagRequired("to")
	if err != nil {
		log.Fatalln(err)
	}

	transactionsCommand.Flags().StringVarP(&outputFormat, "output-format", "o", "json", "Output format")
	transactionsCommand.Flags().StringVarP(&accountID, "account-id", "a", "", "Fetch transactions for this account ID only.")

	var withStatusFlag bool
	var withOptionalMetadataFlag bool
	insitutionCommand := &cobra.Command{
		Use:   "institution [ITEM-ID-OR-ALIAS]",
		Short: "Get information about an institution",
		Long:  "Get information about an institution. Status can be reported using a flag.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			itemOrAlias := args[0]
			itemID, ok := data.Aliases[itemOrAlias]
			if ok {
				itemOrAlias = itemID
			}

			err := WithRelinkOnAuthError(itemOrAlias, linker, func() error {
				token := data.Tokens[itemOrAlias]

				itemReq := plaid.NewItemGetRequest(token)
				ctx := context.Background()
				itemApiReq := client.ItemGet(ctx)
				itemApiReq = itemApiReq.ItemGetRequest(*itemReq)
				itemResp, _, err := itemApiReq.Execute()
				if err != nil {
					return err
				}

				instID := *itemResp.Item.InstitutionId.Get()

				req := plaid.NewInstitutionsGetByIdRequest(instID, countries)
				req.SetOptions(plaid.InstitutionsGetByIdRequestOptions{
					IncludeOptionalMetadata: &withOptionalMetadataFlag,
					IncludeStatus:           &withStatusFlag,
				})
				apiReq := client.InstitutionsGetById(ctx)
				apiReq = apiReq.InstitutionsGetByIdRequest(*req)
				resp, _, err := apiReq.Execute()
				if err != nil {
					return err
				}

				b, err := json.MarshalIndent(resp.Institution, "", "  ")
				if err != nil {
					return err
				}

				fmt.Println(string(b))

				return nil
			})

			if err != nil {
				log.Fatalln(err)
			}
		},
	}
	insitutionCommand.Flags().BoolVarP(&withStatusFlag, "status", "s", false, "Fetch institution status")
	insitutionCommand.Flags().BoolVarP(&withOptionalMetadataFlag, "optional-metadata", "m", false, "Fetch optional metadata like logo and URL")

	rootCommand := &cobra.Command{
		Use:   "plaid-cli",
		Short: "Link bank accounts and get transactions from the command line.",
		Long: `plaid-cli 🤑

plaid-cli is a CLI tool for working with the Plaid API.

You can use plaid-cli to link bank accounts and pull transactions in multiple 
output formats from the comfort of the command line.

Configuration:
  To get started, you'll need Plaid API credentials, which you can get by visiting
  https://dashboard.plaid.com/team/keys after signing up for free.
  
  plaid-cli will look at the following environment variables for API credentials:
  
    PLAID_CLIENT_ID=<client id>
    PLAID_SECRET=<devlopment secret>
    PLAID_ENVIRONMENT=development
    PLAID_LANGUAGE=en  # optional, detected using system's locale
    PLAID_COUNTRIES=US # optional, detected using system's locale
  
  I recommend setting and exporting these on shell startup.
  
  API credentials can also be specified using a config file located at 
  ~/.plaid-cli/config.toml:
  
    [plaid]
    client_id = "<client id>"
    secret = "<development secret>"
    environment = "development"
  
  After setting those API credentials, plaid-cli is ready to use! 
  You'll probably want to run 'plaid-cli link' next.
  
  Please see the README (https://github.com/landakram/plaid-cli/blob/master/README.md) 
  for more detailed usage instructions.

  Made by @landakram.
`,
	}
	rootCommand.AddCommand(linkCommand)
	rootCommand.AddCommand(tokensCommand)
	rootCommand.AddCommand(aliasCommand)
	rootCommand.AddCommand(aliasesCommand)
	rootCommand.AddCommand(accountsCommand)
	rootCommand.AddCommand(transactionsCommand)
	rootCommand.AddCommand(insitutionCommand)

	if !viper.IsSet("plaid.client_id") {
		log.Println("⚠️  PLAID_CLIENT_ID not set. Please see the configuration instructions below.")
		err = rootCommand.Help()
		if err != nil {
			log.Fatalln(err)
		}
		os.Exit(1)
	}
	if !viper.IsSet("plaid.secret") {
		log.Println("⚠️ PLAID_SECRET not set. Please see the configuration instructions below.")
		err = rootCommand.Help()
		if err != nil {
			log.Fatalln(err)
		}
		os.Exit(1)
	}

	err = rootCommand.Execute()
	if err != nil {
		log.Fatalln(err)
	}
}

func AllTransactions(req plaid.TransactionsGetRequest, client *plaid.PlaidApiService) ([]plaid.Transaction, error) {
	var transactions []plaid.Transaction

	ctx := context.Background()
	apiReq := client.TransactionsGet(ctx)
	apiReq = apiReq.TransactionsGetRequest(req)
	res, _, err := apiReq.Execute()
	if err != nil {
		return transactions, err
	}
	transactions = append(transactions, res.Transactions...)

	for len(transactions) < int(res.TotalTransactions) {
		offset := *req.Options.Offset + *req.Options.Count
		req.Options.Offset = &offset
		apiReq = client.TransactionsGet(ctx)
		apiReq = apiReq.TransactionsGetRequest(req)
		res, _, err = apiReq.Execute()
		if err != nil {
			return transactions, err
		}

		transactions = append(transactions, res.Transactions...)
	}

	return transactions, nil
}

func WithRelinkOnAuthError(itemID string, linker *plaid_cli.Linker, action func() error) error {
	err := action()
	pe, convertErr := plaid.ToPlaidError(err)
	if convertErr != nil {
		return errors.Join(err, convertErr)
	}
	if pe.ErrorCode == "ITEM_LOGIN_REQUIRED" {
		log.Println("Login expired. Relinking...")

		port := viper.GetString("link.port")

		err = linker.Relink(itemID, port)

		if err != nil {
			return err
		}

		log.Println("Re-running action...")

		err = action()
	}

	return err
}

type TransactionSerializer interface {
	serialize(txs []plaid.Transaction) ([]byte, error)
}

func NewTransactionSerializer(t string) (TransactionSerializer, error) {
	switch t {
	case "csv":
		return &CSVSerializer{}, nil
	case "json":
		return &JSONSerializer{}, nil
	default:
		return nil, fmt.Errorf("invalid output format: %s", t)
	}
}

type CSVSerializer struct{}

func (w *CSVSerializer) serialize(txs []plaid.Transaction) ([]byte, error) {
	var records [][]string
	for _, tx := range txs {
		sanitizedName := strings.ReplaceAll(tx.Name, ",", "")
		records = append(records, []string{tx.Date, fmt.Sprintf("%f", tx.Amount), sanitizedName})
	}

	b := bytes.NewBufferString("")
	writer := csv.NewWriter(b)
	err := writer.Write([]string{"Date", "Amount", "Description"})
	if err != nil {
		return nil, err
	}
	err = writer.WriteAll(records)
	if err != nil {
		return nil, err
	}

	return b.Bytes(), err
}

func SetAlias(data *plaid_cli.Data, itemID string, alias string) error {
	if _, ok := data.Tokens[itemID]; !ok {
		return fmt.Errorf("no access token found for item ID `%s`. Try re-linking your account with `plaid-cli link`", itemID)
	}

	data.Aliases[alias] = itemID
	data.BackAliases[itemID] = alias
	err := data.Save()
	if err != nil {
		return err
	}

	log.Printf("Aliased %s to %s.", itemID, alias)

	return nil
}

type JSONSerializer struct{}

func (w *JSONSerializer) serialize(txs []plaid.Transaction) ([]byte, error) {
	return json.MarshalIndent(txs, "", "  ")
}
