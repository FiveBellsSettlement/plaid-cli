package plaid_cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"text/template"

	"github.com/plaid/plaid-go/v26/plaid"
	"github.com/skratchdot/open-golang/open"
)

const clientName = "plaid-cli"

var products = []plaid.Products{
	plaid.PRODUCTS_TRANSACTIONS,
	plaid.PRODUCTS_AUTH,
}

type Linker struct {
	Results       chan string
	RelinkResults chan bool
	Errors        chan error
	Client        *plaid.PlaidApiService
	Data          *Data
	countries     []plaid.CountryCode
	lang          string
}

type TokenPair struct {
	ItemID      string
	AccessToken string
}

func (l *Linker) Relink(itemID string, port string) error {
	token := l.Data.Tokens[itemID]
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	ctx := context.Background()
	usr := *plaid.NewLinkTokenCreateRequestUser(hostname)
	req := plaid.NewLinkTokenCreateRequest(clientName, l.lang, l.countries, usr)
	req.SetProducts(products)
	req.SetAccessToken(token)
	// might need to add redirection for oauth
	apiReq := l.Client.LinkTokenCreate(ctx)
	apiReq = apiReq.LinkTokenCreateRequest(*req)
	// consider wrapping http resp for errors
	resp, _, err := apiReq.Execute()
	if err != nil {
		return err
	}
	return l.relink(port, resp.LinkToken)
}

func (l *Linker) Link(port string) (*TokenPair, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	usr := *plaid.NewLinkTokenCreateRequestUser(hostname)
	req := plaid.NewLinkTokenCreateRequest(clientName, l.lang, l.countries, usr)
	req.SetProducts(products)
	// might need to add redirection for oauth
	apiReq := l.Client.LinkTokenCreate(ctx)
	apiReq = apiReq.LinkTokenCreateRequest(*req)
	// consider wrapping http resp for errors
	resp, _, err := apiReq.Execute()
	if err != nil {
		return nil, err
	}

	return l.link(port, resp.LinkToken)
}

func (l *Linker) link(port string, linkToken string) (*TokenPair, error) {
	log.Printf("Starting Plaid Link on port %s...\n", port)

	go func() {
		http.HandleFunc("/link", handleLink(l, linkToken))
		err := http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
		if err != nil {
			l.Errors <- err
		}
	}()

	url := fmt.Sprintf("http://localhost:%s/link", port)
	log.Printf("Your browser should open automatically. If it doesn't, please visit %s to continue linking!", url)
	err := open.Run(url)
	if err != nil {
		log.Printf("Failed to open browser: %v\n", err)
	}

	select {
	case err := <-l.Errors:
		return nil, err
	case publicToken := <-l.Results:

		res, err := l.exchange(publicToken)
		if err != nil {
			return nil, err
		}

		pair := &TokenPair{
			ItemID:      res.ItemId,
			AccessToken: res.AccessToken,
		}

		return pair, nil
	}
}

func (l *Linker) relink(port string, linkToken string) error {
	log.Printf("Starting Plaid Link on port %s...\n", port)

	go func() {
		http.HandleFunc("/relink", handleRelink(l, linkToken))
		err := http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
		if err != nil {
			l.Errors <- err
		}
	}()

	url := fmt.Sprintf("http://localhost:%s/relink", port)
	log.Printf("Your browser should open automatically. If it doesn't, please visit %s to continue linking!", url)
	err := open.Run(url)
	if err != nil {
		log.Printf("Failed to open browser: %v\n", err)
	}

	select {
	case err := <-l.Errors:
		return err
	case <-l.RelinkResults:
		return nil
	}
}

func (l *Linker) exchange(publicToken string) (plaid.ItemPublicTokenExchangeResponse, error) {
	req := plaid.NewItemPublicTokenExchangeRequest(publicToken)
	apiReq := l.Client.ItemPublicTokenExchange(context.Background())
	apiReq = apiReq.ItemPublicTokenExchangeRequest(*req)
	res, _, err := apiReq.Execute()
	return res, err
}

func NewLinker(data *Data, client *plaid.PlaidApiService, countries []plaid.CountryCode, lang string) *Linker {
	return &Linker{
		Results:       make(chan string),
		RelinkResults: make(chan bool),
		Errors:        make(chan error),
		Client:        client,
		Data:          data,
		countries:     countries,
		lang:          lang,
	}
}

func handleLink(linker *Linker, linkToken string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			t := template.New("link")
			t, _ = t.Parse(linkTemplate)

			d := LinkTmplData{
				LinkToken: linkToken,
			}
			err := t.Execute(w, d)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				linker.Errors <- err
			}
		case http.MethodPost:
			err := r.ParseForm()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				linker.Errors <- err
				return
			}
			token := r.Form.Get("public_token")
			if token != "" {
				linker.Results <- token
			} else {
				w.WriteHeader(http.StatusBadRequest)
				linker.Errors <- errors.New("empty public_token")
				return
			}

			_, err = fmt.Fprintf(w, "ok")
			if err != nil {
				linker.Errors <- err
			}
		default:
			linker.Errors <- errors.New("invalid HTTP method")
		}
	}
}

type LinkTmplData struct {
	LinkToken string
}

type RelinkTmplData struct {
	LinkToken string
}

func handleRelink(linker *Linker, linkToken string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			t := template.New("relink")
			t, _ = t.Parse(relinkTemplate)

			d := RelinkTmplData{
				LinkToken: linkToken,
			}
			err := t.Execute(w, d)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				linker.Errors <- err
			}
		case http.MethodPost:
			err := r.ParseForm()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				linker.Errors <- err
				return
			}
			formErr := r.Form.Get("error")
			if formErr != "" {
				linker.Errors <- errors.New(formErr)
			} else {
				linker.RelinkResults <- true
			}

			_, err = fmt.Fprintf(w, "ok")
			if err != nil {
				linker.Errors <- err
			}
		default:
			linker.Errors <- errors.New("invalid HTTP method")
		}
	}
}

var linkTemplate = `<html>
  <head>
    <style>
    .alert-success {
	font-size: 1.2em;
	font-family: Arial, Helvetica, sans-serif;
	background-color: #008000;
	color: #fff;
	display: flex;
	justify-content: center;
	align-items: center;
	border-radius: 15px;
	width: 100%;
	height: 100%;
    }
    .hidden {
	visibility: hidden;
    }
    </style>
  </head>
  <body>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/jquery/2.2.3/jquery.min.js"></script>
    <script src="https://cdn.plaid.com/link/v2/stable/link-initialize.js"></script>
    <script type="text/javascript">
     (function($) {
       var handler = Plaid.create({
	 token: '{{ .LinkToken }}',
	 onSuccess: function(public_token, metadata) {
	   // Send the public_token to your app server.
	   // The metadata object contains info about the institution the
	   // user selected and the account ID or IDs, if the
	   // Select Account view is enabled.
	   $.post('/link', {
	     public_token: public_token,
	   });
	   document.getElementById("alert").classList.remove("hidden");
	 },
	 onExit: function(err, metadata) {
	   // The user exited the Link flow.
	   if (err != null) {
	     // The user encountered a Plaid API error prior to exiting.
	   }
	   // metadata contains information about the institution
	   // that the user selected and the most recent API request IDs.
	   // Storing this information can be helpful for support.

	   document.getElementById("alert").classList.remove("hidden");
	 }
       });

       handler.open();

     })(jQuery);
    </script>

    <div id="alert" class="alert-success hidden">
      <div>
	<h2>All done here!</h2>
	<p>You can close this window and go back to plaid-cli.</p>
      </div>
    </div>
  </body>
</html> `

var relinkTemplate string = `<html>
  <head>
    <style>
    .alert-success {
	font-size: 1.2em;
	font-family: Arial, Helvetica, sans-serif;
	background-color: #008000;
	color: #fff;
	display: flex;
	justify-content: center;
	align-items: center;
	border-radius: 15px;
	width: 100%;
	height: 100%;
    }
    .hidden {
	visibility: hidden;
    }
    </style>
  </head>
  <body>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/jquery/2.2.3/jquery.min.js"></script>
    <script src="https://cdn.plaid.com/link/v2/stable/link-initialize.js"></script>
    <script type="text/javascript">
     (function($) {
       var handler = Plaid.create({
	 token: '{{ .LinkToken }}',
	 onSuccess: (public_token, metadata) => {
	   // You do not need to repeat the /item/public_token/exchange
	   // process when a user uses Link in update mode.
	   // The Item's access_token has not changed.
	 },
	 onExit: function(err, metadata) {
	   if (err != null) {
	     $.post('/relink', {
	       error: err
	     });
	   } else {
	     $.post('/relink', {
	       error: null
	     });
	   }
	   // metadata contains information about the institution
	   // that the user selected and the most recent API request IDs.
	   // Storing this information can be helpful for support.

	   document.getElementById("alert").classList.remove("hidden");
	 }
       });

       handler.open();

     })(jQuery);
    </script>

    <div id="alert" class="alert-success hidden">
      <div>
	<h2>All done here!</h2>
	<p>You can close this window and go back to plaid-cli.</p>
      </div>
    </div>
  </body>
</html>`
