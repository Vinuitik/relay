package api

import (
	"errors"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"relay/runner/internal/collectors"
	"relay/runner/internal/uplink"
)

// Local confirm (/local/confirm): the person at this computer allows or
// denies package installs a collector's approve asked for. Never reachable
// from the phone/hub: localOnly refuses uplink-dispatched requests, any
// non-loopback RemoteAddr, any non-loopback Host (DNS rebinding) and any
// proxy-forwarded request. No X-Relay-Key: being on this machine is the auth.

func (s *Server) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalRequest(r) {
			writeError(w, http.StatusForbidden, "this page is only available on the computer running the runner")
			return
		}
		if s.Packages == nil || s.Packages.Confirm == nil {
			writeError(w, http.StatusNotFound, "no installs are waiting for confirmation on this runner")
			return
		}
		next(w, r)
	}
}

func isLocalRequest(r *http.Request) bool {
	if uplink.FromUplink(r.Context()) {
		return false
	}
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-Ip") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(r.Host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var confirmPage = template.Must(template.New("confirm").Parse(`<!doctype html><meta charset="utf-8">
<title>Relay - confirm installs</title>
<style>body{font:15px system-ui;max-width:42rem;margin:2rem auto;padding:0 1rem}
.c{border:1px solid #8884;border-radius:8px;padding:1rem;margin:1rem 0}button{font:inherit;padding:.4rem 1rem;margin-right:.5rem}</style>
<h1>Confirm installs</h1>
{{range .}}<div class="c" data-token="{{.Token}}"><p>KPI collector <b>{{.CollectorID}}</b> wants to install:</p><ul>
{{range .Requires}}<li><code>{{.Manager}}</code> <b>{{.ID}}</b> - {{.Why}}</li>{{end}}</ul>
<button data-d="allow">Allow</button><button data-d="deny">Deny</button></div>
{{else}}<p>Nothing is waiting.</p>{{end}}
<script>document.querySelectorAll('button[data-d]').forEach(function(b){b.onclick=function(){
var c=b.closest('.c');fetch('/local/confirm/'+c.dataset.token,{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({decision:b.dataset.d})}).then(function(r){c.textContent=r.ok?'Done ('+b.dataset.d+').':'Failed ('+r.status+').'})}})</script>`))

func (s *Server) handleLocalConfirmPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_ = confirmPage.Execute(w, s.Packages.Confirm.List())
}

func (s *Server) handleLocalConfirmDecide(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if err := decodeJSON(r, &req); err != nil || (req.Decision != "allow" && req.Decision != "deny") {
		writeError(w, http.StatusBadRequest, `body must be {"decision":"allow"|"deny"}`)
		return
	}
	switch err := s.Packages.Confirm.Decide(r.PathValue("token"), req.Decision == "allow"); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{})
	case errors.Is(err, collectors.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such pending install (already decided?)")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// NotifyPendingInstall shows a desktop notification that an install waits
// for confirmation. Best effort: any failure is ignored.
func NotifyPendingInstall(p collectors.Pending, listenAddr string) {
	url := LocalConfirmURL(listenAddr)
	msg := "A KPI collector wants to install software. Review it at " + url
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("notify-send", "Relay", msg)
	case "windows":
		ps := `$m=$env:RELAY_MSG;[void][Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime];` +
			`$x=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent('ToastText02');` +
			`$t=$x.GetElementsByTagName('text');$t.Item(0).AppendChild($x.CreateTextNode('Relay'))|Out-Null;$t.Item(1).AppendChild($x.CreateTextNode($m))|Out-Null;` +
			`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Relay').Show([Windows.UI.Notifications.ToastNotification]::new($x))`
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		cmd.Env = append(os.Environ(), "RELAY_MSG="+msg)
	default:
		return
	}
	if cmd.Start() == nil {
		go func() {
			t := time.AfterFunc(15*time.Second, func() { _ = cmd.Process.Kill() })
			_ = cmd.Wait()
			t.Stop()
		}()
	}
}

// LocalConfirmURL is where the person at this computer opens the confirm
// page: always 127.0.0.1 (the local listener), never the Tailscale address.
func LocalConfirmURL(listenAddr string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		port = "7777"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/local/confirm"
}

// LocalListenAddr: the extra 127.0.0.1:<port> listener needed when
// listenAddr binds one specific non-loopback IP ("" = none needed:
// wildcard, empty host or loopback already reach 127.0.0.1).
func LocalListenAddr(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return ""
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// LocalRoutes serves ONLY /local/* (for the loopback-only listener).
func (s *Server) LocalRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/confirm", s.localOnly(s.handleLocalConfirmPage))
	mux.HandleFunc("POST /local/confirm/{token}", s.localOnly(s.handleLocalConfirmDecide))
	return mux
}
