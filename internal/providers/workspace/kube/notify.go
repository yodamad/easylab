package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"easylab/internal/providers/workspace"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// Students spend a workshop in their IDE tab, not on the portal, so an admin's
// message has to reach them there. code-server is served straight through the
// workspace's Ingress with no proxy and no path prefix of EasyLab's, so nothing
// can be injected into its page from outside: the message has to come from
// inside the container.
//
// This writes a two-file VS Code extension into the IDE's extensions directory
// as part of the same best-effort bootstrap that already seeds settings.json and
// installs extensions (see setupSteps). A plain folder with a package.json and a
// JS entry point is all an extension needs, so there is no build step, no .vsix
// to package and no registry to install from.
//
// The extension polls Spec.NotifyURL carrying Spec.NotifyToken and shows what
// comes back with code-server's own notifications. Everything it does is
// best-effort: a workshop must not break because a message endpoint is down.

const (
	// notifyExtensionDir is the extension's folder name under the IDE's extensions
	// directory.
	notifyExtensionDir = "easylab-notify"
	// envNotifyURL and envNotifyToken are how the workspace container is told
	// where to read its notifications and with what. The extension reads them
	// from its own process environment.
	envNotifyURL   = "EASYLAB_NOTIFY_URL"
	envNotifyToken = "EASYLAB_NOTIFY_TOKEN"
	// envTemplate names the workspace template, so the notification endpoint can
	// answer with that template's slides link. Set on new workspaces only and
	// never by reconcileNotifyEnv (a retrofit would restart running workspaces);
	// an older workspace simply lacks it.
	envTemplate = "EASYLAB_TEMPLATE"
)

// notifyExtensionStep returns the bootstrap lines that install the in-IDE
// notification extension, or "" when the workspace has nowhere to poll.
//
// Unlike userSettingsStep this overwrites on every start: the files are
// EasyLab's, not the student's, so a workspace picks up a newer version of the
// extension — or a changed URL or token — simply by restarting.
func notifyExtensionStep(spec workspace.Spec, p ideProfile) string {
	url := strings.TrimSpace(spec.NotifyURL)
	if url == "" || !p.supportsExtensions {
		return ""
	}

	manifest, err := json.MarshalIndent(notifyExtensionManifest(), "", "  ")
	if err != nil {
		// Only a programming error could get here; a workspace must still start.
		return ""
	}

	var b strings.Builder
	b.WriteString("e=\"${XDG_DATA_HOME:-$HOME/.local/share}/code-server/extensions\"; ")
	b.WriteString("d=\"$e/" + notifyExtensionDir + "\"; ")
	b.WriteString("{ mkdir -p \"$d\" && ")
	fmt.Fprintf(&b, "printf '%%s\\n' %s > \"$d/package.json\" && ", shellQuote(string(manifest)))
	fmt.Fprintf(&b, "printf '%%s\\n' %s > \"$d/extension.js\" && ", shellQuote(notifyExtensionSource))
	// extensions.json is the IDE's cache of what it found in this directory last
	// time, and a folder dropped in behind its back is not in it. Removing it
	// before the IDE starts makes it scan the directory again, which is also how
	// the extensions a template installed are found.
	b.WriteString("rm -f \"$e/extensions.json\"")
	b.WriteString("; } || true\n")
	return b.String()
}

// ReconcileNotify implements workspace.NotifyReconciler, so the background pass
// that retrofits running workspaces can reach the same reconcile EnsureWorkspace
// does on its way past an existing one.
func (b *Backend) ReconcileNotify(ctx context.Context, workspaceID string, spec workspace.Spec) error {
	return b.reconcileNotifyEnv(ctx, workspaceID, spec)
}

// reconcileNotifyEnv brings an already-running workspace's notification endpoint
// in line with spec, so a workspace created before the lab had an address to
// poll — or before the feature existed — starts receiving messages instead of
// staying deaf until someone deletes it. EnsureWorkspace returns an existing
// Deployment untouched, so without this the pod template is never revisited.
//
// Changing the pod template restarts the pod, which is what writes the extension
// (see notifyExtensionStep), so this is deliberately narrow:
//
//   - an empty NotifyURL changes nothing and never strips what is there. It means
//     "no address right now", which a sync can report transiently, and acting on
//     it would restart every workspace in the lab for nothing;
//   - values that already match change nothing, so the common path — a student
//     polling their own workspace status — issues no write at all.
//
// With a stable signing key the token is a deterministic HMAC over lab and
// owner, so a workspace converges after exactly one restart and then stays put.
// The deployment strategy is Recreate (the volume is ReadWriteOnce), so the
// restart is a clean stop-then-start rather than a wedged rollout.
func (b *Backend) reconcileNotifyEnv(ctx context.Context, name string, spec workspace.Spec) error {
	url := strings.TrimSpace(spec.NotifyURL)
	if url == "" {
		return nil
	}

	deployments := b.client.AppsV1().Deployments(b.namespace)
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		dep, err := deployments.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to look up workspace %s: %w", name, err)
		}

		changed = false
		for i := range dep.Spec.Template.Spec.Containers {
			c := &dep.Spec.Template.Spec.Containers[i]
			if c.Name != workspaceContainerName {
				continue
			}
			if setEnvValue(&c.Env, envNotifyURL, url) {
				changed = true
			}
			if setEnvValue(&c.Env, envNotifyToken, spec.NotifyToken) {
				changed = true
			}
		}
		if !changed {
			return nil
		}

		_, err = deployments.Update(ctx, dep, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile notifications on workspace %s: %w", name, err)
	}
	if changed {
		log.Printf("Pointed workspace %s at %s for notifications; it restarts to pick the extension up", name, url)
	}
	return nil
}

// setEnvValue sets one environment entry on a container, reporting whether that
// changed anything. A duplicated name is collapsed to the one the kubelet would
// apply — the last — so a template that set the same variable does not leave two
// behind that disagree.
func setEnvValue(env *[]corev1.EnvVar, name, value string) bool {
	last := -1
	for i := range *env {
		if (*env)[i].Name == name {
			last = i
		}
	}
	if last >= 0 {
		if (*env)[last].Value == value && (*env)[last].ValueFrom == nil {
			return false
		}
		(*env)[last] = corev1.EnvVar{Name: name, Value: value}
		return true
	}
	*env = append(*env, corev1.EnvVar{Name: name, Value: value})
	return true
}

// notifyExtensionManifest is the extension's package.json. onStartupFinished
// activates it once the IDE is up without delaying that startup. The one
// contribution is the command that opens the message board, so a student who
// dismissed the notification can still reach it from the command palette.
func notifyExtensionManifest() map[string]any {
	return map[string]any{
		"name":        notifyExtensionDir,
		"displayName": "EasyLab notifications",
		"description": "Shows the messages your workshop admin sends.",
		"version":     "1.0.0",
		"publisher":   "easylab",
		"engines":     map[string]any{"vscode": "^1.60.0"},
		// "*" alongside onStartupFinished so an older code-server that does not
		// know the newer event still activates the extension.
		"activationEvents": []string{"onStartupFinished", "*"},
		"main":             "./extension.js",
		// The board is reachable from the command palette as well as from the
		// status bar, so a student who dismissed a message can still find it.
		// Asking for help is contributed the same way: a student in a terminal
		// reaches it from the palette without hunting for the status bar.
		"contributes": map[string]any{
			"commands": []map[string]any{{
				"command":  "easylab.showMessages",
				"title":    "Show workshop messages",
				"category": "EasyLab",
			}, {
				"command":  "easylab.askForHelp",
				"title":    "Ask for help",
				"category": "EasyLab",
			}, {
				"command":  "easylab.openSlides",
				"title":    "Open slides",
				"category": "EasyLab",
			}},
		},
	}
}

// notifyExtensionSource is the extension itself. It runs in the extension host,
// which is Node, so it reads its configuration from the environment the
// container was given and uses Node's own http/https rather than a dependency.
//
// Deliberate properties:
//   - every failure is swallowed, including a missing URL or token: the student's
//     IDE must behave exactly as before when notifications are unavailable;
//   - ids already shown are remembered for the life of the window, so a message
//     is not shown again on every poll;
//   - the response body is capped, because the IDE is talking to a URL over the
//     network and must not grow its own memory on a bad answer.
const notifyExtensionSource = `const vscode = require('vscode');
const http = require('http');
const https = require('https');
const { URL, URLSearchParams } = require('url');

const POLL_MS = 20000;
const MAX_BYTES = 64 * 1024;
// STATUS_MAX keeps the beacon legible on a narrow status bar. The whole message
// is in its tooltip and on the board, so nothing is lost by clipping here.
const STATUS_MAX = 48;
const BOARD_COMMAND = "easylab.showMessages";
const BOARD_TITLE = "Workshop messages";
const OPEN_LABEL = "Open messages";
const HELP_COMMAND = "easylab.askForHelp";
const HELP_TITLE = "Ask for help";
// HELP_MAX matches the server's own cap, so a student is told here rather than
// having their note quietly clipped.
const HELP_MAX = 300;
const SLIDES_COMMAND = "easylab.openSlides";
const SLIDES_SEEN_KEY = "easylab.slidesSeen";

// seen gates the toast, so a message pops exactly once per window. live is the
// current set, replaced wholesale on every poll: the server stops returning a
// message once it expires, and that is what clears it from the board and the
// status bar. Nothing here is ever dismissed by the student.
const seen = new Set();
let live = [];
let timer = null;
let status = null;
let panel = null;
// helpEndpoint is where "Ask for help" posts, derived from the notification URL
// (see activate). Null when this workspace has nowhere to reach, in which case
// the command says so rather than failing silently.
let helpEndpoint = null;
let helpStatus = null;
// slidesStatus is the status bar entry for the template's slides, and
// slidesLink the http(s) link the last poll returned (null when there is none).
let slidesStatus = null;
let slidesLink = null;
let globalState = null;

function fetchNotifications(endpoint) {
    return new Promise((resolve) => {
        let target;
        try {
            target = new URL(endpoint);
        } catch (e) {
            resolve(null);
            return;
        }
        const client = target.protocol === 'http:' ? http : https;
        const req = client.get(target, { timeout: 10000 }, (res) => {
            if (res.statusCode !== 200) {
                res.resume();
                resolve(null);
                return;
            }
            let body = '';
            res.setEncoding('utf8');
            res.on('data', (chunk) => {
                if (body.length > MAX_BYTES) {
                    req.destroy();
                    return;
                }
                body += chunk;
            });
            res.on('end', () => {
                try {
                    resolve(JSON.parse(body));
                } catch (e) {
                    resolve(null);
                }
            });
        });
        req.on('timeout', () => req.destroy());
        req.on('error', () => resolve(null));
    });
}

// postForm sends one form-encoded POST and resolves to whether the server
// accepted it. Same discipline as fetchNotifications: Node's own http/https, a
// timeout, and every failure swallowed into a plain false.
function postForm(endpoint, fields) {
    return new Promise((resolve) => {
        let target;
        try {
            target = new URL(endpoint);
        } catch (e) {
            resolve(false);
            return;
        }
        const body = new URLSearchParams(fields).toString();
        const client = target.protocol === 'http:' ? http : https;
        const req = client.request(target, {
            method: 'POST',
            timeout: 10000,
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
                'Content-Length': Buffer.byteLength(body)
            }
        }, (res) => {
            const ok = res.statusCode >= 200 && res.statusCode < 300;
            res.resume();
            res.on('end', () => resolve(ok));
        });
        req.on('timeout', () => req.destroy());
        req.on('error', () => resolve(false));
        req.write(body);
        req.end();
    });
}

// askForHelp is the student raising their hand from where they actually are. The
// note is optional: dismissing the box without typing still sends the request,
// because being stuck is the message.
async function askForHelp() {
    if (!helpEndpoint) {
        vscode.window.showWarningMessage('EasyLab: asking for help is not available in this workspace. Use the student portal instead.');
        return;
    }
    let note;
    try {
        note = await vscode.window.showInputBox({
            title: HELP_TITLE,
            prompt: 'What do you need help with? (optional)',
            placeHolder: 'My build fails with a permission error',
            ignoreFocusOut: true
        });
    } catch (e) {
        return;
    }
    // Escape means "never mind"; an empty answer means "just come over".
    if (note === undefined) {
        return;
    }
    const ok = await postForm(helpEndpoint, { message: clip(note, HELP_MAX) });
    if (ok) {
        vscode.window.showInformationMessage('EasyLab: your organiser has been told you need help.');
    } else {
        vscode.window.showWarningMessage('EasyLab: could not reach your organiser. Try again, or ask from the student portal.');
    }
}

function clip(value, max) {
    const text = String(value || '').replace(/\s+/g, ' ').trim();
    return text.length > max ? text.slice(0, max - 1) + '…' : text;
}

// minutesLeft is how the student is told about time: a count down, not a clock
// time they would have to subtract from. Null when the server sent no expiry.
function minutesLeft(item) {
    const end = Date.parse(item.expires_at);
    if (isNaN(end)) {
        return null;
    }
    return Math.max(0, Math.round((end - Date.now()) / 60000));
}

function timeLeftLabel(item) {
    const mins = minutesLeft(item);
    if (mins === null) {
        return '';
    }
    if (mins < 1) {
        return 'less than a minute left';
    }
    return mins + ' min left';
}

// updateStatus keeps a beacon in the status bar for as long as any message is
// up. It is the part that survives: a toast is gone in seconds, and a student
// deep in a terminal needs something still there when they look up. The latest
// message rides in the beacon itself rather than a count, because the text is
// what they actually want at a glance.
function updateStatus() {
    if (!status) {
        return;
    }
    if (!live.length) {
        status.hide();
        return;
    }
    const urgent = live.some((item) => item.level === 'warning');
    status.text = '$(megaphone) ' + clip(live[0].message, STATUS_MAX);
    status.backgroundColor = urgent ? new vscode.ThemeColor('statusBarItem.warningBackground') : undefined;

    const lines = [live.length === 1 ? 'One message from your instructor' : live.length + ' messages from your instructor', ''];
    for (const item of live) {
        const left = timeLeftLabel(item);
        lines.push('• ' + clip(item.message, 160) + (left ? '  (' + left + ')' : ''));
    }
    lines.push('');
    lines.push('Click to open ' + BOARD_TITLE);
    // A plain string, not a MarkdownString: the body is the admin's own words and
    // must never be parsed as markup here.
    status.tooltip = lines.join('\n');
    status.show();
}

// openBoard shows the message board beside whatever the student is working on.
// preserveFocus is the whole point of the restraint: an announcement must not
// take the cursor out of their editor mid-keystroke.
function openBoard() {
    if (panel) {
        panel.reveal(vscode.ViewColumn.Beside, true);
        renderBoard();
        return;
    }
    panel = vscode.window.createWebviewPanel(
        'easylabMessages',
        BOARD_TITLE,
        { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
        { enableScripts: true, retainContextWhenHidden: true }
    );
    panel.onDidDispose(() => { panel = null; });
    panel.webview.html = boardHtml(require('crypto').randomBytes(16).toString('hex'));
    renderBoard();
}

function renderBoard() {
    if (!panel) {
        return;
    }
    // The board gets the data and keeps its own clock, so the rails and the
    // countdowns stay honest between polls.
    panel.webview.postMessage({ items: live });
}

function boardHtml(nonce) {
    const csp = "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-" + nonce + "';";
    return [
        '<!DOCTYPE html>',
        '<html lang="en">',
        '<head>',
        '<meta charset="utf-8">',
        '<meta http-equiv="Content-Security-Policy" content="' + csp + '">',
        '<style>',
        ':root {',
        '  --ground: var(--vscode-editor-background);',
        '  --ink: var(--vscode-foreground);',
        '  --quiet: var(--vscode-descriptionForeground, var(--vscode-foreground));',
        '  --seam: var(--vscode-panel-border, rgba(128, 128, 128, 0.25));',
        '  --live: var(--vscode-textLink-foreground, #4daafc);',
        '  --urgent: var(--vscode-editorWarning-foreground, #cca700);',
        '}',
        '* { box-sizing: border-box; }',
        'body {',
        '  margin: 0;',
        '  padding: 2rem 1.75rem 3rem;',
        '  background: var(--ground);',
        '  color: var(--ink);',
        '  font-family: var(--vscode-font-family);',
        '  font-size: var(--vscode-font-size, 13px);',
        '  line-height: 1.5;',
        '}',
        '.msg {',
        '  position: relative;',
        '  padding: 0 0 1.75rem 1.5rem;',
        '  margin: 0 0 1.75rem;',
        '  border-bottom: 1px solid var(--seam);',
        '}',
        '.msg:last-child { margin-bottom: 0; padding-bottom: 0; border-bottom: 0; }',
        /* The rail is the remaining life of the message drawn against its whole
           life, so several together show which announcement is about to go. Its
           track is a fixed height on purpose: tied to the row it would shrink
           with a short message, and two rails would no longer be comparable.
           It duplicates the countdown in text, so it is hidden from readers. */
        '.rail {',
        '  position: absolute;',
        '  left: 0;',
        '  top: 0.4em;',
        '  width: 3px;',
        '  height: 46px;',
        '}',
        '.rail::before {',
        '  content: "";',
        '  position: absolute;',
        '  inset: 0;',
        '  background: var(--live);',
        '  opacity: 0.22;',
        '}',
        '.rail i {',
        '  position: absolute;',
        '  left: 0;',
        '  top: 0;',
        '  width: 100%;',
        '  background: var(--live);',
        '  transition: height 0.6s linear;',
        '}',
        '.msg.urgent .rail::before, .msg.urgent .rail i { background: var(--urgent); }',
        '.text {',
        '  margin: 0;',
        '  font-size: 1.45em;',
        '  line-height: 1.4;',
        '  max-width: 62ch;',
        '}',
        '.meta {',
        '  display: flex;',
        '  flex-wrap: wrap;',
        '  gap: 0.35rem 1.5rem;',
        '  margin-top: 0.9rem;',
        '  font-family: var(--vscode-editor-font-family, monospace);',
        '  font-size: 0.85em;',
        '  color: var(--quiet);',
        '}',
        '.meta .left { color: var(--live); }',
        '.msg.urgent .meta .left { color: var(--urgent); }',
        '.empty { margin: 0; max-width: 48ch; color: var(--quiet); }',
        '@media (max-width: 420px) { body { padding: 1.25rem 1rem 2rem; } .text { font-size: 1.25em; } }',
        '@media (prefers-reduced-motion: reduce) { .rail i { transition: none; } }',
        '</style>',
        '</head>',
        '<body>',
        '<main id="board"></main>',
        '<script nonce="' + nonce + '">',
        'var root = document.getElementById("board");',
        'var items = [];',
        'function leftLabel(remaining) {',
        '  if (remaining <= 0) { return "expired"; }',
        '  var mins = Math.round(remaining / 60000);',
        '  if (mins < 1) { return "less than a minute left"; }',
        '  return mins + " min left";',
        '}',
        'function sentLabel(value) {',
        '  var at = new Date(value);',
        '  if (isNaN(at.getTime())) { return ""; }',
        '  return "sent " + at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });',
        '}',
        'function render() {',
        '  root.textContent = "";',
        '  if (!items.length) {',
        '    var empty = document.createElement("p");',
        '    empty.className = "empty";',
        '    empty.textContent = "No messages right now. Announcements from your instructor appear here while the workshop runs, and clear themselves when they run out of time.";',
        '    root.appendChild(empty);',
        '    return;',
        '  }',
        '  for (var i = 0; i < items.length; i++) {',
        '    var item = items[i];',
        '    var span = Date.parse(item.expires_at) - Date.parse(item.at);',
        '    var remaining = Date.parse(item.expires_at) - Date.now();',
        '    var pct = span > 0 ? Math.max(0, Math.min(100, (remaining / span) * 100)) : 0;',
        '    // A message with time left always shows a mark: the one about to go is',
        '    // the one that most needs to be seen, and it is the one nearest zero.',
        '    if (pct > 0 && pct < 7) { pct = 7; }',
        '    var el = document.createElement("article");',
        '    el.className = item.level === "warning" ? "msg urgent" : "msg";',
        '    var rail = document.createElement("div");',
        '    rail.className = "rail";',
        '    rail.setAttribute("aria-hidden", "true");',
        '    var fill = document.createElement("i");',
        '    fill.style.height = pct.toFixed(1) + "%";',
        '    rail.appendChild(fill);',
        '    var text = document.createElement("p");',
        '    text.className = "text";',
        '    text.textContent = item.message;',
        '    var meta = document.createElement("div");',
        '    meta.className = "meta";',
        '    var sent = document.createElement("span");',
        '    sent.textContent = sentLabel(item.at);',
        '    var left = document.createElement("span");',
        '    left.className = "left";',
        '    left.textContent = isNaN(remaining) ? "" : leftLabel(remaining);',
        '    meta.appendChild(sent);',
        '    meta.appendChild(left);',
        '    el.appendChild(rail);',
        '    el.appendChild(text);',
        '    el.appendChild(meta);',
        '    root.appendChild(el);',
        '  }',
        '}',
        'window.addEventListener("message", function (event) {',
        '  var data = event.data || {};',
        '  items = Array.isArray(data.items) ? data.items : [];',
        '  render();',
        '});',
        'setInterval(render, 30000);',
        'render();',
        '</script>',
        '</body>',
        '</html>'
    ].join('\n');
}

// toast is the arrival signal. It names the sender, because a bare sentence
// appearing in an editor reads as the editor talking; and it offers the board,
// so a message worth re-reading is one click away rather than gone.
function toast(item) {
    const message = clip(item.message, 500);
    if (!message) {
        return;
    }
    const label = 'Instructor: ' + message;
    const shown = item.level === 'warning'
        ? vscode.window.showWarningMessage(label, OPEN_LABEL)
        : vscode.window.showInformationMessage(label, OPEN_LABEL);
    shown.then((choice) => {
        if (choice === OPEN_LABEL) {
            openBoard();
        }
    }, () => {});
}

// safeLink returns the link when it is an absolute http(s) URL, else null. The
// server validates it too; this is the last check before it is opened.
function safeLink(value) {
    if (typeof value !== 'string' || !value) {
        return null;
    }
    try {
        const u = new URL(value);
        return (u.protocol === 'http:' || u.protocol === 'https:') ? value : null;
    } catch (e) {
        return null;
    }
}

function openSlides() {
    if (!slidesLink) {
        vscode.window.showInformationMessage('No slides are linked to this workspace.');
        return;
    }
    vscode.env.openExternal(vscode.Uri.parse(slidesLink));
}

function updateSlides(link) {
    slidesLink = link;
    if (!slidesStatus) {
        return;
    }
    if (!link) {
        slidesStatus.hide();
        return;
    }
    let host = '';
    try {
        host = new URL(link).hostname;
    } catch (e) {}
    slidesStatus.text = '$(book) Slides';
    slidesStatus.tooltip = 'Open the workshop slides (' + host + ')';
    slidesStatus.show();
    // Announced once per distinct link, remembered across windows.
    const known = globalState ? globalState.get(SLIDES_SEEN_KEY) : link;
    if (known !== link) {
        if (globalState) {
            globalState.update(SLIDES_SEEN_KEY, link);
        }
        vscode.window.showInformationMessage('Your workshop slides are available.', 'Open slides').then((choice) => {
            if (choice === 'Open slides') {
                openSlides();
            }
        }, () => {});
    }
}

async function poll(endpoint) {
    const payload = await fetchNotifications(endpoint);
    if (!payload || !Array.isArray(payload.notifications)) {
        // A failed poll says nothing about what is up: leave the board and the
        // beacon showing what they already show.
        return;
    }
    const items = payload.notifications.filter((item) => item && item.id && item.message);
    live = items;
    for (const item of items) {
        if (seen.has(item.id)) {
            continue;
        }
        seen.add(item.id);
        toast(item);
    }
    updateStatus();
    renderBoard();
    updateSlides(safeLink(payload.resource_link));
}

function activate(context) {
    const keep = (item) => {
        if (context && context.subscriptions) {
            context.subscriptions.push(item);
        }
    };
    // Registered whether or not this workspace has an endpoint, so the palette
    // entries the manifest contributes always resolve to something.
    keep(vscode.commands.registerCommand(BOARD_COMMAND, openBoard));
    keep(vscode.commands.registerCommand(HELP_COMMAND, askForHelp));
    keep(vscode.commands.registerCommand(SLIDES_COMMAND, openSlides));
    globalState = context && context.globalState ? context.globalState : null;

    const base = process.env.EASYLAB_NOTIFY_URL;
    const token = process.env.EASYLAB_NOTIFY_TOKEN;
    if (!base || !token) {
        return;
    }
    // Left of the status bar at a high priority, next to the indicators a
    // student already watches, rather than off among the language and encoding
    // readouts on the right.
    status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 1000);
    status.name = BOARD_TITLE;
    status.command = BOARD_COMMAND;
    keep(status);
    let endpoint;
    let pollEndpoint;
    try {
        const url = new URL(base);
        url.searchParams.set('token', token);
        endpoint = url.toString();
        pollEndpoint = endpoint;
        if (process.env.EASYLAB_TEMPLATE) {
            pollEndpoint += '&template=' + encodeURIComponent(process.env.EASYLAB_TEMPLATE);
        }
        // Asking for help is the same server, the same lab and the same token —
        // only a different path. Deriving it means the container needs no second
        // variable, so a workspace is never restarted just to learn this URL.
        const help = new URL(endpoint);
        help.pathname = help.pathname.replace(/\/notifications$/, '/help');
        helpEndpoint = help.pathname === url.pathname ? null : help.toString();
    } catch (e) {
        return;
    }
    if (helpEndpoint) {
        // Right of the board's beacon, and always visible: a student who is stuck
        // should not have to discover a command to say so.
        helpStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 999);
        helpStatus.name = HELP_TITLE;
        helpStatus.command = HELP_COMMAND;
        helpStatus.text = '$(question) ' + HELP_TITLE;
        helpStatus.tooltip = 'Tell your workshop organiser you need help';
        keep(helpStatus);
        helpStatus.show();
    }
    // Left, next to the beacon; hidden until a poll returns a link.
    slidesStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 998);
    slidesStatus.name = 'Workshop slides';
    slidesStatus.command = SLIDES_COMMAND;
    keep(slidesStatus);
    const tick = () => {
        poll(pollEndpoint).catch(() => {}).then(() => {
            timer = setTimeout(tick, POLL_MS);
        });
    };
    tick();
}

function deactivate() {
    if (timer) {
        clearTimeout(timer);
        timer = null;
    }
}

module.exports = { activate, deactivate };
`
