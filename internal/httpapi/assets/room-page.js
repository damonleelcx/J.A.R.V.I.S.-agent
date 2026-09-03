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

  function renderTurns(turns) {
    var box = $('turns');
    box.textContent = '';
    turns.forEach(appendTurn);
    box.scrollTop = box.scrollHeight;
  }

  function appendTurn(t) {
    var box = $('turns');
    var row = doc.createElement('div');
    row.className = 'turn' + (t.redacted ? ' redacted' : '');
    row.setAttribute('data-seq', String(t.seq));

    var who = doc.createElement('span');
    who.className = 'turn-who';
    // The label AS RECORDED, not resolved now. A renamed account must not
    // silently rewrite who said something six months ago.
    who.textContent = t.speaker === 'forge' ? 'FORGE' : (t.speaker_label || 'unknown');

    var mark = doc.createElement('span');
    mark.className = 'turn-mark';
    // Spoken and typed are marked differently, because "was that said aloud in
    // the meeting or typed afterwards" is a question people actually ask of a
    // transcript.
    mark.textContent = t.channel === 'voice' ? 'spoke' : 'typed';

    var text = doc.createElement('span');
    text.className = 'turn-text';
    if (t.redacted) {
      // The row survives with its speaker and time so the transcript does not
      // read as though these seconds were silent. Saying so plainly is the whole
      // point of keeping it.
      text.textContent = 'deleted' + (t.redacted_by ? ' by ' + t.redacted_by : '');
      text.className += ' gone';
    } else {
      text.textContent = t.text;
    }

    row.appendChild(who);
    row.appendChild(mark);
    row.appendChild(text);
    box.appendChild(row);
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
