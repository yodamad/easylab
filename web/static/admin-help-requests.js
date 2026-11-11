// Keeps the "Help requests" count in the admin sidebar current, and raises a
// desktop notification when a student starts waiting — so an organiser whose
// EasyLab tab is behind their slides still finds out.
//
// It is loaded from the admin-sidebar template, so it runs on every admin page.
// That is why it is JS rather than HTMX: HTMX is only pulled in by four of those
// pages, and the Notification API has no declarative equivalent anyway. The
// poll is jittered for the same reason as the student one in
// student-notifications.js — several admin tabs open on a workshop should not
// hit the endpoint in lockstep.

const HELP_POLL_BASE_MS = 20000;
const HELP_POLL_JITTER_MS = 5000;

// Ids already announced in this tab. sessionStorage rather than localStorage: a
// request is worth one notification per browsing session, and a tab opened later
// in the workshop should still catch whoever is waiting now. Every access is
// guarded — it throws in a private window with site data blocked.
const HELP_SEEN_KEY = 'easylab_seen_help_requests';

let _helpPollTimer = null;

function helpSeenIds() {
    try {
        const raw = sessionStorage.getItem(HELP_SEEN_KEY);
        return raw ? new Set(JSON.parse(raw)) : new Set();
    } catch (e) {
        return new Set();
    }
}

function rememberHelpSeen(ids) {
    try {
        // Capped: a long workshop must not grow this without bound.
        sessionStorage.setItem(HELP_SEEN_KEY, JSON.stringify(Array.from(ids).slice(-200)));
    } catch (e) {
        /* nothing to do: the badge still updated, the alert may just repeat */
    }
}

// helpNotificationsSupported reports whether this browser can raise a desktop
// notification at all. Served over plain HTTP it is missing, and the badge is
// then the only channel.
function helpNotificationsSupported() {
    return typeof window !== 'undefined' && 'Notification' in window;
}

// updateHelpBell reflects the permission state on the bell, so an admin can tell
// whether alerts will actually arrive. The bell only exists on the Help requests
// page itself — permission is asked for once, there.
function updateHelpBell() {
    const bell = document.getElementById('help-bell');
    if (!bell) return;

    if (!helpNotificationsSupported()) {
        bell.classList.add('is-unavailable');
        bell.title = 'Desktop notifications are not available in this browser';
        bell.setAttribute('aria-label', bell.title);
        return;
    }

    bell.classList.remove('help-bell--granted', 'help-bell--denied');
    if (Notification.permission === 'granted') {
        bell.classList.add('help-bell--granted');
        bell.title = 'Desktop notifications are on';
    } else if (Notification.permission === 'denied') {
        bell.classList.add('help-bell--denied');
        bell.title = 'Desktop notifications are blocked for this site';
    } else {
        bell.title = 'Turn on desktop notifications';
    }
    bell.setAttribute('aria-label', bell.title);
}

// requestHelpNotifyPermission asks on a click rather than on load: browsers want
// the prompt tied to a gesture, and an unexplained prompt the moment a page opens
// is the one people dismiss.
async function requestHelpNotifyPermission() {
    if (!helpNotificationsSupported()) return;
    try {
        await Notification.requestPermission();
    } catch (e) {
        console.error('Failed to request notification permission:', e);
    }
    updateHelpBell();
}

// showHelpDesktopNotification raises the OS-level notification. The request id is
// the tag, so several open admin tabs show one notification between them instead
// of one each.
function showHelpDesktopNotification(request) {
    if (!helpNotificationsSupported() || Notification.permission !== 'granted') return;
    try {
        const who = request.student || 'A student';
        const body = request.message
            ? who + ' (' + request.lab_name + '): ' + request.message
            : who + ' needs help in ' + request.lab_name;
        new Notification('EasyLab · help needed', { body: body, tag: request.id });
    } catch (e) {
        console.error('Failed to show notification:', e);
    }
}

// updateHelpBadge shows the count beside the sidebar item, and hides the pill
// entirely at zero rather than showing a "0" that reads like a problem.
function updateHelpBadge(count) {
    const badge = document.getElementById('help-requests-badge');
    if (!badge) return;
    if (!count) {
        badge.textContent = '';
        badge.classList.add('is-empty');
        badge.removeAttribute('title');
        return;
    }
    badge.textContent = String(count);
    badge.classList.remove('is-empty');
    badge.title = count === 1 ? '1 student is waiting for help' : count + ' students are waiting for help';
}

async function pollHelpRequests() {
    try {
        const resp = await fetch('/api/admin/help-requests', { credentials: 'same-origin' });
        if (!resp.ok) return;
        const payload = await resp.json();
        const items = Array.isArray(payload.requests) ? payload.requests : [];
        updateHelpBadge(typeof payload.count === 'number' ? payload.count : items.length);

        const seen = helpSeenIds();
        let added = false;
        items.forEach(item => {
            if (!item || !item.id || seen.has(item.id)) return;
            seen.add(item.id);
            added = true;
            showHelpDesktopNotification(item);
        });
        if (added) {
            rememberHelpSeen(seen);
        }
    } catch (e) {
        // A failed poll is not worth telling the admin about; the next one may
        // well succeed.
        console.error('Failed to load help requests:', e);
    }
}

function scheduleHelpPoll() {
    const delay = HELP_POLL_BASE_MS + Math.random() * HELP_POLL_JITTER_MS;
    _helpPollTimer = setTimeout(async () => {
        await pollHelpRequests();
        scheduleHelpPoll();
    }, delay);
}

document.addEventListener('DOMContentLoaded', () => {
    const bell = document.getElementById('help-bell');
    if (bell) {
        bell.addEventListener('click', requestHelpNotifyPermission);
    }
    updateHelpBell();
    pollHelpRequests();
    scheduleHelpPoll();
});
