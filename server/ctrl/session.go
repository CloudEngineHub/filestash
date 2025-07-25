package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/template"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/middleware"
	"github.com/mickael-kerjean/filestash/server/model"

	"github.com/gorilla/mux"
)

type Session struct {
	Home          *string `json:"home,omitempty"`
	IsAuth        bool    `json:"is_authenticated"`
	Backend       string  `json:"backendID"`
	Authorization string  `json:"authorization,omitempty"`
}

func SessionGet(ctx *App, res http.ResponseWriter, req *http.Request) {
	r := Session{
		IsAuth: false,
	}
	if ctx.Backend == nil {
		SendSuccessResult(res, r)
		return
	}
	home, err := model.GetHome(ctx.Backend, ctx.Session["path"])
	if err != nil {
		SendSuccessResult(res, r)
		return
	} else if ctx.Share.Id != "" {
		home = "/"
	}
	r.IsAuth = true
	r.Home = NewString(home)
	r.Backend = Hash(GenerateID(ctx.Session)+ctx.Session["path"], 20)
	if ctx.Share.Id == "" && Config.Get("features.protection.enable_chromecast").Bool() {
		r.Authorization = ctx.Authorization
	}
	SendSuccessResult(res, r)
}

func SessionAuthenticate(ctx *App, res http.ResponseWriter, req *http.Request) {
	ctx.Body["timestamp"] = time.Now().Format(time.RFC3339)
	session := model.MapStringInterfaceToMapStringString(ctx.Body)
	session["path"] = EnforceDirectory(session["path"])

	backend, err := model.NewBackend(ctx, session)
	if err != nil {
		Log.Debug("session::auth 'NewBackend' %+v", err)
		SendErrorResult(res, err)
		return
	}

	if obj, ok := backend.(interface {
		OAuthToken(*map[string]interface{}) error
	}); ok {
		err := obj.OAuthToken(&ctx.Body)
		if err != nil {
			Log.Debug("session::auth 'OAuthToken' %+v", err)
			SendErrorResult(res, NewError("Can't authenticate (OAuth error)", 401))
			return
		}
		session = model.MapStringInterfaceToMapStringString(ctx.Body)
		backend, err = model.NewBackend(ctx, session)
		if err != nil {
			Log.Debug("session::auth 'OAuthToken::NewBackend' %+v", err)
			SendErrorResult(res, NewError("Can't authenticate", 401))
			return
		}
	}

	home, err := model.GetHome(backend, session["path"])
	if err != nil {
		Log.Debug("session::auth 'GetHome' %+v", err)
		SendErrorResult(res, ErrAuthenticationFailed)
		return
	}

	s, err := json.Marshal(session)
	if err != nil {
		Log.Debug("session::auth 'Marshal' %+v", err)
		SendErrorResult(res, NewError(err.Error(), 500))
		return
	}
	obfuscate, err := EncryptString(SECRET_KEY_DERIVATE_FOR_USER, string(s))
	if err != nil {
		Log.Debug("session::auth 'Encryption' %+v", err)
		SendErrorResult(res, NewError(err.Error(), 500))
		return
	}
	// split session cookie if greater than 3800 bytes
	value_limit := 3800
	index := 0
	end := 0
	for {
		if len(obfuscate) >= (index+1)*value_limit {
			end = (index + 1) * value_limit
		} else {
			end = len(obfuscate)
		}
		http.SetCookie(res, applyCookieRules(&http.Cookie{
			Name:   CookieName(index),
			Value:  obfuscate[index*value_limit : end],
			MaxAge: 60 * Config.Get("general.cookie_timeout").Int(),
			Path:   COOKIE_PATH,
		}, req))
		if end == len(obfuscate) {
			break
		} else {
			Log.Debug("session::auth obfuscate index: %d length: %d total: %d", index, len(obfuscate[index*value_limit:end]), len(obfuscate))
			index++
		}
	}
	if Config.Get("features.protection.iframe").String() != "" {
		res.Header().Set("bearer", obfuscate)
	}
	if home != "" {
		SendSuccessResult(res, home)
		return
	}
	SendSuccessResult(res, nil)
}

func SessionLogout(ctx *App, res http.ResponseWriter, req *http.Request) {
	go func() {
		// user typically expect the logout to feel instant but in our case we still need to make sure
		// the connection is closed as lot of backend requires to hold an active session which we cache.
		// Whenever somebody logout after say 30 minutes idle, the logout would first create a connection
		// then close which can take a few seconds and make for a bad user experience.
		// By pushing that connection close in a goroutine, we make sure the logout is much faster for
		// the user while still retaining that functionality.
		middleware.SessionTry(func(c *App, _res http.ResponseWriter, _req *http.Request) {
			if c.Backend != nil {
				if obj, ok := c.Backend.(interface{ Close() error }); ok {
					obj.Close()
				}
			}
		})(ctx, res, req)
	}()
	index := 0
	for {
		_, err := req.Cookie(CookieName(index))
		if err != nil {
			break
		}
		http.SetCookie(res, applyCookieRules(&http.Cookie{
			Name:   CookieName(index),
			Value:  "",
			MaxAge: -1,
			Path:   COOKIE_PATH,
		}, req))
		index++
	}
	http.SetCookie(res, &http.Cookie{
		Name:   COOKIE_NAME_ADMIN,
		Value:  "",
		MaxAge: -1,
		Path:   COOKIE_PATH_ADMIN,
	})
	http.SetCookie(res, &http.Cookie{
		Name:   COOKIE_NAME_PROOF,
		Value:  "",
		MaxAge: -1,
		Path:   COOKIE_PATH,
	})
	SendSuccessResult(res, nil)
}

func SessionOAuthBackend(ctx *App, res http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	a := map[string]string{
		"type": vars["service"],
	}
	b, err := model.NewBackend(ctx, a)
	if err != nil {
		Log.Debug("session::oauth 'NewBackend' %+v", err)
		SendErrorResult(res, err)
		return
	}
	obj, ok := b.(interface{ OAuthURL() string })
	if ok == false {
		Log.Debug("session::oauth 'Backend does not support oauth - \"%s\"'", a["type"])
		SendErrorResult(res, ErrNotSupported)
		return
	}
	redirectUrl, err := url.Parse(obj.OAuthURL())
	if err != nil {
		Log.Debug("session::oauth 'Parse URL - \"%s\"'", a["type"])
		SendErrorResult(res, ErrNotValid)
		return
	}
	stateValue := vars["service"]
	if req.URL.Query().Get("next") != "" {
		stateValue += "::" + req.URL.Query().Get("next")
	}
	q := redirectUrl.Query()
	q.Set("state", stateValue)
	redirectUrl.RawQuery = q.Encode()
	if strings.Contains(req.Header.Get("Accept"), "text/html") {
		http.Redirect(res, req, redirectUrl.String(), http.StatusSeeOther)
		return
	}
	SendSuccessResult(res, redirectUrl.String())
}

func SessionAuthMiddleware(ctx *App, res http.ResponseWriter, req *http.Request) {
	SSOCookieName := "ssoref"

	// Step0: Initialisation
	_get := req.URL.Query()
	plugin := func() IAuthentication {
		selectedPluginId := Config.Get("middleware.identity_provider.type").String()
		if selectedPluginId == "" {
			return nil
		}
		for key, plugin := range Hooks.Get.AuthenticationMiddleware() {
			if key == selectedPluginId {
				return plugin
			}
		}
		return nil
	}()
	if plugin == nil {
		http.Redirect(
			res, req,
			"/?error=Not%20Found&trace=middleware not found",
			http.StatusTemporaryRedirect,
		)
		return
	}
	formData := map[string]string{}
	for key, element := range _get {
		if len(element) == 0 {
			continue
		}
		formData[key] = element[0]
	}
	if req.Method == http.MethodPost {
		if err := req.ParseForm(); err != nil {
			http.Redirect(
				res, req,
				"/?error=Not%20Valid&trace=parsing body - "+err.Error(),
				http.StatusTemporaryRedirect,
			)
			return
		}
		for key, values := range req.Form {
			if len(values) == 0 {
				continue
			}
			formData[key] = values[0]
		}
	}

	idpParams := map[string]string{}
	if err := json.Unmarshal(
		[]byte(Config.Get("middleware.identity_provider.params").String()),
		&idpParams,
	); err != nil {
		http.Redirect(
			res, req,
			"/?error=Not%20Valid&trace=unpacking idp - "+err.Error(),
			http.StatusTemporaryRedirect,
		)
		return
	}

	// Step1: Entrypoint of the authentication process is handled by the plugin
	if req.Method == "GET" && _get.Get("action") == "redirect" {
		if label := _get.Get("label"); label != "" {
			http.SetCookie(
				res,
				applyCookieSameSiteRule(
					applyCookieRules(&http.Cookie{
						Name:   SSOCookieName,
						Value:  label + "::" + _get.Get("state"),
						MaxAge: 60 * 10,
						Path:   COOKIE_PATH,
					}, req),
					http.SameSiteDefaultMode,
				),
			)
		}
		if err := plugin.EntryPoint(idpParams, req, res); err != nil {
			Log.Error("entrypoint - %s", err.Error())
			res.Header().Set("Content-Type", "text/html; charset=utf-8")
			res.WriteHeader(http.StatusOK)
			res.Write([]byte(Page(err.Error())))
		}
		return
	}

	// Step2: End of the authentication process. Could come from:
	// - target of a html form. eg: ldap, mysql, ...
	// - identity provider redirection uri. eg: oauth2, openid, ...
	templateBind, err := plugin.Callback(formData, idpParams, res)
	if err == ErrAuthenticationFailed {
		Log.Warning("failed authentication - %s", err.Error())
		http.Redirect(
			res, req,
			req.URL.Path+"?action=redirect",
			http.StatusSeeOther,
		)
		return
	} else if err != nil {
		Log.Error("session::authMiddleware 'callback error - %s'", err.Error())
		http.Redirect(
			res, req,
			"/?error="+ErrNotAllowed.Error()+"&trace=redirect request failed - "+err.Error(),
			http.StatusSeeOther,
		)
		return
	}
	templateBind["machine_id"] = GenerateMachineID()
	for _, value := range os.Environ() {
		pair := strings.SplitN(value, "=", 2)
		if len(pair) == 2 {
			templateBind[fmt.Sprintf("ENV_%s", pair[0])] = pair[1]
		}
	}

	var (
		label = ""
		state = ""
	)
	if refCookie, err := req.Cookie(SSOCookieName); err == nil { // TODO: deprecate SSOCookieName
		s := strings.SplitN(refCookie.Value, "::", 2)
		switch len(s) {
		case 1:
			label = s[0]
		case 2:
			label = s[0]
			state = s[1]
		}
	} else if l := req.URL.Query().Get("label"); l != "" {
		label = l
		state = req.URL.Query().Get("state")
	} else {
		Log.Warning("session::authMiddleware action=callback_error err=missing_label url=%s", req.URL.String())
	}
	if decodedState, err := base64.StdEncoding.DecodeString(state); err == nil {
		stateStruct := map[string]string{}
		json.Unmarshal(decodedState, &stateStruct)

		// check variables are "legit"
		attributes := ""
		signature := ""
		fields := strings.Split(Config.Get("features.protection.signature").String(), ",")
		for k, v := range stateStruct {
			if k == "signature" {
				signature = v
			}
			if slices.Contains(fields, k) {
				attributes += fmt.Sprintf("%s[%s] ", k, v)
			}
		}
		if attributes = strings.TrimSpace(attributes); attributes != "" {
			v, err := DecryptString(SECRET_KEY_DERIVATE_FOR_SIGNATURE, signature)
			if err != nil || attributes != v {
				v, _ = EncryptString(SECRET_KEY_DERIVATE_FOR_SIGNATURE, attributes)
				Log.Debug("callback signature is required, signature=%s", v)
				http.Redirect(
					res, req,
					WithBase("/?error=Invalid%20Signature&trace=signature is not correct"),
					http.StatusTemporaryRedirect,
				)
				return
			}
		}

		// populate variable
		for key, value := range stateStruct {
			if templateBind[key] != "" {
				continue
			}
			templateBind[key] = value
		}
	}
	redirectURI := templateBind["next"]
	if redirectURI == "" {
		redirectURI = WithBase("/")
	}
	if templateBind["nav"] != "" {
		redirectURI += "?nav=" + templateBind["nav"]
	}

	// Step3: create a backend connection object
	session, err := func(tb map[string]string) (map[string]string, error) {
		globalMapping := map[string]map[string]interface{}{}
		if err = json.Unmarshal(
			[]byte(Config.Get("middleware.attribute_mapping.params").String()),
			&globalMapping,
		); err != nil {
			Log.Warning("session::authMiddlware 'attribute mapping error' %s", err.Error())
			return map[string]string{}, err
		}
		mappingToUse := map[string]string{}
		for k, v := range globalMapping[label] {
			str := NewStringFromInterface(v)
			if str == "" {
				continue
			}
			tmpl, err := template.
				New("ctrl::session::auth_middleware").
				Funcs(tmplFuncs).
				Parse(str)
			mappingToUse[k] = str
			if err != nil {
				Log.Debug("session::authMiddleware 'template creation failed %s'", err.Error())
				continue
			}
			var b bytes.Buffer
			if err = tmpl.Execute(&b, tb); err != nil {
				Log.Debug("session::authMiddleware 'template execution failed %s'", err.Error())
				continue
			}
			mappingToUse[k] = b.String()
		}
		mappingToUse["timestamp"] = time.Now().Format(time.RFC3339)
		return mappingToUse, nil
	}(templateBind)
	if err != nil {
		Log.Debug("session::authMiddleware 'auth mapping failed %s'", err.Error())
		http.Redirect(
			res, req,
			WithBase("/?error=Not%20Valid&trace=mapping_error - "+err.Error()),
			http.StatusTemporaryRedirect,
		)
		return
	}

	if _, err := model.NewBackend(ctx, session); err != nil {
		Log.Debug("session::authMiddleware 'backend connection failed %+v - %s'", session, err.Error())
		url := "/?error=" + ErrNotValid.Error() + "&trace=backend error - " + err.Error()
		if IsATranslatedError(err) {
			url = "/?error=" + err.Error() + "&trace=backend error - " + err.Error()
		}
		http.Redirect(res, req, WithBase(url), http.StatusTemporaryRedirect)
		return
	}

	// Step4: persist connection with a cookie
	s, err := json.Marshal(session)
	if err != nil {
		Log.Debug("session::authMiddleware 'session marshal error %+v'", session)
		SendErrorResult(res, ErrNotValid)
		return
	}
	obfuscate, err := EncryptString(SECRET_KEY_DERIVATE_FOR_USER, string(s))
	if err != nil {
		Log.Debug("session::authMiddleware 'encryption error - %s", err.Error())
		SendErrorResult(res, ErrNotValid)
		return
	}
	http.SetCookie(res, applyCookieRules(&http.Cookie{ // TODO: deprecate SSOCookieName
		Name:   SSOCookieName,
		Value:  "",
		MaxAge: -1,
		Path:   COOKIE_PATH,
	}, req))
	http.SetCookie(res, applyCookieRules(&http.Cookie{
		Name:   COOKIE_NAME_AUTH,
		Value:  obfuscate,
		MaxAge: 60 * Config.Get("general.cookie_timeout").Int(),
		Path:   COOKIE_PATH,
	}, req))
	if Config.Get("features.protection.iframe").String() != "" {
		redirectURI += "#bearer=" + obfuscate
	}
	http.Redirect(res, req, redirectURI, http.StatusSeeOther)
}

func applyCookieRules(cookie *http.Cookie, req *http.Request) *http.Cookie {
	cookie.HttpOnly = true
	cookie.SameSite = http.SameSiteStrictMode
	if Config.Get("features.protection.iframe").String() != "" {
		if f := req.Header.Get("Referer"); strings.HasPrefix(f, "https://") {
			cookie.Secure = true
			cookie.SameSite = http.SameSiteNoneMode
			cookie.Partitioned = true
		} else {
			Log.Warning("you are trying to access Filestash from a non secure origin ('%s') and with iframe enabled. Either use SSL or disable iframe from the admin console.", f)
		}
	}
	return cookie
}

func applyCookieSameSiteRule(cookie *http.Cookie, sameSiteValue http.SameSite) *http.Cookie {
	cookie.SameSite = sameSiteValue
	return cookie
}
