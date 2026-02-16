package plg_widget_chat

import (
	_ "embed"
	"net/http"

	. "github.com/mickael-kerjean/filestash/server/common"
	. "github.com/mickael-kerjean/filestash/server/middleware"

	"github.com/gorilla/mux"
)

//go:embed assets/sidebar_chat.js
var CTRLJS []byte

//go:embed assets/sidebar.diff
var PATCH []byte

func init() {
	Hooks.Register.HttpEndpoint(func(r *mux.Router) error {
		r.HandleFunc("/api/plg_widget_chat/messages", NewMiddlewareChain(createMessage, []Middleware{ApiHeaders, SecureHeaders, SessionStart, LoggedInOnly, BodyParser})).Methods("POST")
		r.HandleFunc("/api/plg_widget_chat/messages", NewMiddlewareChain(listMessages, []Middleware{ApiHeaders, SecureHeaders, SessionStart, LoggedInOnly})).Methods("GET")
		r.HandleFunc("/api/plg_widget_chat/lookup", NewMiddlewareChain(lookupUsers, []Middleware{ApiHeaders, SecureHeaders, SessionStart, LoggedInOnly})).Methods("GET")

		r.HandleFunc(WithBase("/plg_handler_chat/sidebar_chat.js"), func(res http.ResponseWriter, req *http.Request) {
			http.Redirect(res, req, WithBase("/assets/"+BUILD_REF+"/components/sidebar_chat.js"), http.StatusSeeOther)
		})
		r.HandleFunc(WithBase("/assets/"+BUILD_REF+"/components/sidebar_chat.js"), func(res http.ResponseWriter, req *http.Request) {
			res.Header().Set("Content-Type", "application/javascript")
			res.Write(CTRLJS)
		}).Methods("GET")
		return nil
	})

	Hooks.Register.StaticPatch(PATCH)
}
