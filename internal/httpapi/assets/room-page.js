/* FORGE room page — the UI over ForgeRoom.
 *
 * Rendering only. Every rule about what a room IS lives on the server or in
 * room.js; this file turns events into elements and buttons into calls. The
 * split matters because the privacy sentence, the attribution and the transcript
 * ordering are all things two clients must never disagree about.
 */
(function (global) {
  'use strict';

  var doc = global.document;
  function $(id) { return doc.getElementById(id); }

  var roomID = doc.body.getAttribute('data-room') || '';
  if (!roomID) return;

  var room = new global.ForgeRoom.Room(roomID, { on: handle });
  var me = null;
  var audioJoined = false;

  /* ---- rendering ---------------------------------------------------------- */

  function setStatus(text) { $('room-status').textContent = text; }

  function showError(message) {
    var el = $('err');
    el.textContent = message;
    el.classList.remove('hidden');
    // Errors here are things a person must act on — a mute the server refused, a
    // room that cannot be read. They do not auto-dismiss.
  }

  function clearError() { $('err').classList.add('hidden'); }

  /* renderPolicy is SEC-06's visible state.
   *
   * The sentence comes from the server so it cannot drift between clients. The
   * dot carries the same information without relying on reading — and without
   * relying on colour alone, since the text always says it too.
   */
  function renderPolicy() {
    $('policy-text').textContent = room.audioPolicy ||
      'What happens to what is said here is not known yet.';
    var dot = $('policy-dot');
    dot.className = 'room-policy__dot' + (room.transcribing ? ' on' : ' off');
    $('policy').setAttribute('data-transcribing', room.transcribing ? 'yes' : 'no');

    var btn = $('transcribe');
    btn.textContent = room.transcribing ? 'Stop transcribing' : 'Start transcribing';
    btn.setAttribute('aria-pressed', room.transcribing ? 'true' : 'false');
  }

  function renderRoom(data) {
    $('room-title').textContent = data.title || 'Room';
    setStatus(data.status === 'open' ? 'live' : data.status);
    renderPolicy();
    renderRoster(data.participants || []);
    renderTurns(data.turns || []);
  }

  function renderRoster(people) {
    var ul = $('roster');
    ul.textContent = '';
    people.filter(function (p) { return p.present; }).forEach(function (p) {
      var li = doc.createElement('li');
      li.className = 'roster-row';
      li.setAttribute('data-user', p.user_id);

      var dot = doc.createElement('span');
      dot.className = 'roster-dot';
      dot.setAttribute('aria-hidden', 'true');

      var name = doc.createElement('span');
      // The roster shows the identifier the record uses. It is not a display
      // name lookup: the transcript deliberately keeps the label as it was when
      // somebody spoke, and inventing a second naming rule here would let the
      // roster and the transcript disagree about who somebody is.
      name.className = 'roster-name';
      name.textContent = p.user_id === me ? p.user_id + ' (you)' : p.user_id;

      var state = doc.createElement('span');
      state.className = 'roster-state';
      state.id = 'sp-' + p.user_id;
      state.textContent = room.speaking[p.user_id] ? 'speaking' : '';

      li.appendChild(dot);
      li.appendChild(name);
      li.appendChild(state);
      ul.appendChild(li);
    });
  }

  /* ---- the transcript, and searching it (PRD AUD-06) ----------------------
   *
   * `turns` is the whole record as the server last gave it. The DOM shows a
   * SUBSET of it whenever a search is active, so the array — not the DOM — is
   * what everything else reads. A turn arriving mid-search still joins the
   * record, and clearing the search brings it back without asking the server
   * again.
   *
   * Why the search is here and not an endpoint, and what was verified in a
   * browser: docs/bugfix/2026-09-03-the-transcript-could-not-be-searched.md
   */
  var turns = [];
  var query = '';

  function renderTurns(list) {
    turns = list.slice();
    rebuild();
  }

  /* matches is deliberately narrow: the TEXT of what was said, nothing else.
   *
   * A redacted turn can never match. Its content is gone, and the word its row
   * shows instead — "deleted" — belongs to this interface, not to anybody in the
   * room. Matching on it would be searching our own vocabulary and presenting
   * the hit as something a person said. */
  function matches(t) {
    if (!query) return true;
    if (t.redacted) return false;
    return String(t.text || '').toLowerCase().indexOf(query.toLowerCase()) !== -1;
  }

  function matchCount() {
    var n = 0;
    turns.forEach(function (t) { if (matches(t)) n++; });
    return n;
  }

  function rebuild() {
    var box = $('turns');
    // #turns is a live region for turns ARRIVING. A filter rewrites every row at
    // once, and without this a screen reader reads the whole transcript back on
    // every keystroke. aria-busy is the attribute for exactly that: hold
    // announcements until the batch is done.
    box.setAttribute('aria-busy', 'true');
    box.textContent = '';
    turns.forEach(function (t) {
      if (matches(t)) box.appendChild(turnRow(t));
    });
    box.removeAttribute('aria-busy');
    announce();
    // Live tail when nothing is filtered; the first match when something is.
    box.scrollTop = query ? 0 : box.scrollHeight;
  }

  /* What a screen reader actually needs after typing: how much of the record is
   * still in front of it. The denominator is there so a search that matches
   * nothing is distinguishable from a transcript that is empty. */
  function announce() {
    var el = $('find-count');
    if (!query) { el.textContent = ''; return; }
    var n = matchCount();
    el.textContent = n === 0
      ? 'No turns match "' + query + '".'
      : n + ' of ' + turns.length + ' turns match "' + query + '".';
  }

  function setQuery(next) {
    query = String(next || '').trim();
    $('find-clear').hidden = query === '';
    rebuild();
  }

  function turnRow(t) {
    var row = doc.createElement('div');
    row.className = 'turn' + (t.redacted ? ' redacted' : '');
    row.setAttribute('data-seq', String(t.seq));

    var who = doc.createElement('span');
    who.className = 'turn-who';
    // The label AS RECORDED, not resolved now. A renamed account must not
    // silently rewrite who said something six months ago.
    who.textContent = t.speaker === 'forge' ? 'FORGE' : (t.speaker_label || 'unknown');

    // Named `channel`, not `mark`: this row now also builds <mark> elements for
    // search hits, and two different marks in one function is a trap.
    var channel = doc.createElement('span');
    channel.className = 'turn-mark';
    // Spoken and typed are marked differently, because "was that said aloud in
    // the meeting or typed afterwards" is a question people actually ask of a
    // transcript.
    channel.textContent = t.channel === 'voice' ? 'spoke' : 'typed';

    var text = doc.createElement('span');
    text.className = 'turn-text';
    if (t.redacted) {
      // The row survives with its speaker and time so the transcript does not
      // read as though these seconds were silent. Saying so plainly is the whole
      // point of keeping it.
      text.textContent = 'deleted' + (t.redacted_by ? ' by ' + t.redacted_by : '');
      text.className += ' gone';
    } else {
      fillHighlighted(text, String(t.text || ''));
    }

    row.appendChild(who);
    row.appendChild(channel);
    row.appendChild(text);
    return row;
  }

  /* Writes the text with matched runs wrapped in <mark>, as DOM nodes.
   *
   * Built rather than assigned as markup. This page uses no innerHTML anywhere,
   * and a transcript is the last place to start: every character of it is
   * something a person typed or said. */
  function fillHighlighted(el, text) {
    if (!query) { el.textContent = text; return; }
    var hay = text.toLowerCase();
    var needle = query.toLowerCase();
    var from = 0;
    var at;
    el.textContent = '';
    while ((at = hay.indexOf(needle, from)) !== -1) {
      if (at > from) el.appendChild(doc.createTextNode(text.slice(from, at)));
      var hit = doc.createElement('mark');
      hit.textContent = text.slice(at, at + needle.length);
      el.appendChild(hit);
      from = at + needle.length;
    }
    if (from < text.length) el.appendChild(doc.createTextNode(text.slice(from)));
  }

  function appendTurn(t) {
    turns.push(t);
    if (!matches(t)) {
      // It arrived while a search is narrowing the view. It is in the record,
      // and the count moves, so the room does not look like nothing happened.
      announce();
      return;
    }
    var box = $('turns');
    box.appendChild(turnRow(t));
    announce();
    box.scrollTop = box.scrollHeight;
  }

  function markSpeaking(userID, on) {
    var el = $('sp-' + userID);
    if (el) el.textContent = on ? 'speaking' : '';
    var row = doc.querySelector('.roster-row[data-user="' + cssEscape(userID) + '"]');
    if (row) row.classList.toggle('is-speaking', !!on);
  }

  // Ids are server-minted and alphanumeric, but a selector built from a value is
  // a selector built from a value.
  function cssEscape(s) { return String(s).replace(/["\\]/g, '\\$&'); }

  /* ---- microphones -------------------------------------------------------- */

  function listMics() {
    return global.ForgeAudioInput.devices().then(function (list) {
      var sel = $('micpick');
      sel.textContent = '';
      if (!list.length) {
        var none = doc.createElement('option');
        none.textContent = 'No microphone found';
        none.value = '';
        sel.appendChild(none);
        sel.disabled = true;
        return;
      }
      sel.disabled = false;
      list.forEach(function (d) {
        var o = doc.createElement('option');
        o.value = d.id;
        o.textContent = d.label;
        sel.appendChild(o);
      });
      if (!list[0].labelled) {
        // Explained rather than left as a mystery list: browsers withhold device
        // NAMES until permission has been granted once, so a picker shown before
        // that says "Microphone 1" and cannot say more.
        note('Microphone names appear after you join audio once — browsers ' +
             'withhold them until then.');
      }
    });
  }

  function note(text) { $('micnote').textContent = text; }

  /* ---- events from the room ---------------------------------------------- */

  function handle(kind, data) {
    switch (kind) {
      case 'ready':
        me = data.user_id;
        setStatus('live');
        renderPolicy();
        $('joinaudio').disabled = !data.media_enabled;
        if (!data.media_enabled) {
          note('This deployment carries no audio. Everything here works by typing.');
        } else if (!global.ForgeRoom.supported) {
          $('joinaudio').disabled = true;
          note('This browser has no WebRTC, so it cannot carry room audio. Typing works.');
        }
        listMics();
        break;

      case 'room':
        renderRoom(data);
        break;

      case 'turn':
        appendTurn(data);
        break;

      case 'speaking':
        markSpeaking(data.user_id, data.speaking);
        break;

      case 'policy':
        renderPolicy();
        // Announced, not just re-coloured. Somebody may be part-way through a
        // sentence they believe is off the record.
        showError(data.transcribing
          ? 'This room is now being transcribed. ' + (room.audioPolicy || '')
          : 'This room is no longer being transcribed. ' + (room.audioPolicy || ''));
        break;

      case 'redacted':
        // Said out loud. A transcript quietly losing lines is exactly the shape
        // of the failure the record is meant to prevent, so the reason is given.
        showError('Some spoken turns were deleted by ' + (data.by || 'a participant') +
                  '. The record still shows that they spoke.');
        break;

      case 'mic':
        audioJoined = true;
        $('mute').disabled = false;
        $('pause').disabled = false;
        $('joinaudio').disabled = true;
        listMics(); // names are available now that permission was granted
        if (data.concerns && data.concerns.length) {
          // What the device actually did, not what was asked for.
          note(data.concerns.join(' '));
        } else if (data.settings) {
          note('Using ' + (data.settings.label || 'the selected microphone') +
               ' with echo cancellation and noise suppression.');
        }
        break;

      case 'blocked':
        showError('Your browser blocked audio playback until you interact with ' +
                  'the page. Click anywhere to hear the room.');
        break;

      case 'closed':
        setStatus('ended');
        audioJoined = false;
        ['mute', 'pause', 'transcribe', 'joinaudio'].forEach(function (id) {
          $(id).disabled = true;
        });
        showError('This session has ended. The transcript is still readable.');
        break;

      case 'lagged':
        // Said out loud rather than handled quietly: the person should know the
        // view they were reading had a gap, even though it has now been refilled.
        showError('This connection fell behind and the room was re-read. ' +
                  'Nothing is missing from the transcript below.');
        break;

      case 'offline':
        setStatus('reconnecting');
        break;

      case 'error':
        showError(data.message);
        break;
    }
  }

  /* ---- controls ----------------------------------------------------------- */

  function wire() {
    $('joinaudio').addEventListener('click', function () {
      clearError();
      note('Asking for the microphone…');
      room.joinAudio($('micpick').value || undefined).catch(function (err) {
        // A refused microphone is not a broken room. Said as such, with the path
        // that still works.
        note('');
        showError(err.message + ' You can still take part by typing.');
      });
    });

    // AUD-06's transcript search. Filtering on every keystroke rather than on
    // submit: the transcript is already in memory, so there is nothing to wait
    // for, and a search box that needs Enter is one more thing to know.
    $('find').addEventListener('input', function () { setQuery(this.value); });

    // Escape clears from the keyboard; the button clears without one and returns
    // focus, so neither path strands anybody.
    $('find').addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && this.value !== '') {
        this.value = '';
        setQuery('');
      }
    });

    $('find-clear').addEventListener('click', function () {
      var box = $('find');
      box.value = '';
      setQuery('');
      box.focus();
    });

    $('mute').addEventListener('click', function () {
      var next = room.state === 'muted' ? 'active' : 'muted';
      room.setState(next);
      this.setAttribute('aria-pressed', next === 'muted' ? 'true' : 'false');
      this.textContent = next === 'muted' ? 'Unmute' : 'Mute';
    });

    $('pause').addEventListener('click', function () {
      var next = room.state === 'paused' ? 'active' : 'paused';
      room.setState(next);
      this.setAttribute('aria-pressed', next === 'paused' ? 'true' : 'false');
      this.textContent = next === 'paused' ? 'Resume' : 'Pause';
      // Leaving mute showing "Unmute" while paused would be two controls
      // claiming different things about one microphone.
      $('mute').textContent = 'Mute';
      $('mute').setAttribute('aria-pressed', 'false');
    });

    $('transcribe').addEventListener('click', function () {
      clearError();
      room.setTranscribing(!room.transcribing).catch(function (err) {
        showError(err.message);
      });
    });

    $('delvoice').addEventListener('click', function () {
      clearError();
      // Confirmed, because it cannot be undone. The wording says exactly what
      // survives, so nobody deletes expecting the record to forget they spoke.
      var ok = global.confirm(
        'Delete everything you have said aloud in this room?\n\n' +
        'The words go. The record still shows that you spoke, when, and that the ' +
        'content was deleted by you — so the transcript does not read as though ' +
        'those seconds were silent.\n\nThis cannot be undone.');
      if (!ok) return;
      room.deleteVoice('me').then(function (out) {
        showError('Deleted ' + out.redacted + ' spoken turn(s). ' + (out.effect || ''));
      }).catch(function (err) { showError(err.message); });
    });

    $('leave').addEventListener('click', function () {
      room.leave().then(function () { setStatus('left'); });
    });

    $('sayform').addEventListener('submit', function (e) {
      e.preventDefault();
      var input = $('say');
      var text = input.value.trim();
      if (!text) return;
      input.value = '';
      room.say(text).catch(function (err) {
        input.value = text; // do not silently swallow what they wrote
        showError(err.message);
      });
    });

    // The microphone is released when the tab goes, so the operating system's
    // recording indicator goes out with it.
    global.addEventListener('pagehide', function () { room.hangUp(); });
  }

  wire();
  room.connect();
  global.ForgeRoomPage = { room: room };
})(window);
