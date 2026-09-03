/* FORGE room — the browser side of a shared session.
 *
 * PRD COL-01 (multi-user room with identified speakers), AUD-03 (mic selection,
 * echo cancellation, noise suppression), AUD-06 (a non-audio path for every
 * critical interaction), AUD-07 (always-visible controls), SEC-06 (visible
 * state, deletion).
 *
 * # The rule that shapes every read below
 *
 * The event stream is a HINT. The room record is the truth. On connect, and
 * again after any `lagged` event, the whole room is re-read and re-rendered;
 * events only save a poll in between. A client that treated the stream as
 * authoritative would eventually render a transcript with a hole in it and have
 * no way to discover that — which is exactly what the server's lag signal exists
 * to prevent, and it only works if the client acts on it.
 *
 * # Why the transcript is not built from what this browser hears
 *
 * Speech is transcribed on the server, once, from each participant's own stream.
 * A client transcribing what it hears would produce a second, disagreeing
 * account of the same conversation, attributed by guesswork — and the room
 * record must have exactly one answer to "who said that".
 *
 * # Ordering: control stream first, audio second
 *
 * The stream mints this connection's id, and media signalling is addressed to
 * it. Audio cannot be requested before `hello` arrives because there would be
 * nowhere to send the offers.
 */
(function (global) {
  'use strict';

  var RTC = global.RTCPeerConnection;

  function Room(roomID, opts) {
    opts = opts || {};
    this.roomID = roomID;
    this.on = opts.on || function () {};

    this.streamID = null;
    this.userID = null;
    this.mediaEnabled = false;
    this.transcribing = false;
    this.audioPolicy = '';

    this.pc = null;
    this.mic = null;
    this.state = 'active';       // active | muted | paused
    this.remote = {};            // source stream id -> HTMLAudioElement
    this.speaking = {};          // user id -> boolean
    this.es = null;
    this.closed = false;
  }

  /* ---- the control stream ------------------------------------------------ */

  /* connect reads the room first, THEN opens the stream.
   *
   * The order is deliberate and was chosen after watching it fail. EventSource
   * reports no status code — an unauthenticated caller gets `onerror`, which is
   * indistinguishable from a dropped network, and the browser then retries
   * forever. A signed-out visitor saw "reconnecting" for as long as they were
   * willing to wait, with the real reason (401) visible only in devtools.
   *
   * A plain fetch first turns that into a sentence. It costs one request, on a
   * path that is read again on connect anyway.
   */
  Room.prototype.connect = function () {
    var self = this;
    if (this.es) return;

    this.reload().then(function (room) {
      if (!room) return; // reload already reported why
      self.openStream();
    });
  };

  Room.prototype.openStream = function () {
    var self = this;
    if (this.es) return;

    this.es = new EventSource('/v1/rooms/' + encodeURIComponent(this.roomID) + '/events');

    this.es.addEventListener('hello', function (e) {
      var d = JSON.parse(e.data);
      self.streamID = d.stream_id;
      self.userID = d.user_id;
      self.mediaEnabled = !!d.media_enabled;
      self.transcribing = !!d.transcribing;
      self.audioPolicy = d.audio_policy || '';
      // Read again now the stream is open, so anything written between the
      // first read and the subscription arrives rather than being missed.
      self.reload();
      self.on('ready', d);
    });

    this.es.addEventListener('turn', function (e) {
      self.on('turn', JSON.parse(e.data));
    });

    this.es.addEventListener('presence', function (e) {
      // Presence changes who is in the room, which changes the roster and the
      // set of audio elements. Re-read rather than patched: the record already
      // computes "present at this instant" and a second implementation here
      // would eventually disagree with it.
      self.reload();
      self.on('presence', JSON.parse(e.data));
    });

    this.es.addEventListener('speaking', function (e) {
      var d = JSON.parse(e.data);
      self.speaking[d.user_id] = !!d.speaking;
      self.on('speaking', d);
    });

    this.es.addEventListener('transcribing', function (e) {
      var d = JSON.parse(e.data);
      self.transcribing = !!d.transcribing;
      self.audioPolicy = d.audio_policy || self.audioPolicy;
      // Loudly, and without waiting for a re-read: somebody may be part-way
      // through a sentence they believe is off the record.
      self.on('policy', d);
    });

    this.es.addEventListener('redacted', function (e) {
      // Somebody deleted what they said. Re-read: this browser is still showing
      // words the record no longer holds.
      self.reload();
      self.on('redacted', JSON.parse(e.data));
    });

    this.es.addEventListener('media-offer', function (e) {
      self.answerOffer(JSON.parse(e.data).sdp);
    });

    this.es.addEventListener('closed', function (e) {
      self.closed = true;
      self.hangUp();
      self.on('closed', JSON.parse(e.data));
    });

    this.es.addEventListener('lagged', function () {
      // Told we fell behind. The only correct response is to re-read the record;
      // continuing from here would leave a gap nobody could see.
      self.on('lagged', {});
      self.reload();
      self.reconnect();
    });

    this.es.onerror = function () {
      // EventSource reconnects on its own. Reported so the room can say it is
      // out of touch rather than looking merely quiet — a silent room and a
      // disconnected one must not appear the same.
      if (!self.closed) self.on('offline', {});
    };
  };

  Room.prototype.reconnect = function () {
    if (this.es) { this.es.close(); this.es = null; }
    if (!this.closed) this.openStream();
  };

  Room.prototype.reload = function () {
    var self = this;
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID))
      .then(function (r) {
        if (r.status === 401) {
          throw new Error('You are not signed in, so this room cannot be opened.');
        }
        if (r.status === 403 || r.status === 404) {
          throw new Error('This room is not available to you.');
        }
        if (!r.ok) throw new Error('the room could not be read (' + r.status + ')');
        return r.json();
      })
      .then(function (room) {
        self.transcribing = !!room.transcribing;
        self.audioPolicy = room.audio_policy || '';
        self.on('room', room);
        return room;
      })
      .catch(function (err) {
        self.on('error', { message: err.message });
        return null;
      });
  };

  /* ---- audio -------------------------------------------------------------- */

  /* joinAudio opens the microphone and offers it to the room.
   *
   * Deliberately NOT automatic on page load. A page that grabs a microphone
   * because it was opened is the behaviour every browser has spent a decade
   * teaching people to distrust, and AUD-06 requires the room to be fully usable
   * without one.
   */
  Room.prototype.joinAudio = function (deviceId) {
    var self = this;
    if (!RTC) {
      return Promise.reject(new Error('This browser has no WebRTC, so it cannot carry room audio. Typing works.'));
    }
    if (!this.mediaEnabled) {
      return Promise.reject(new Error('This deployment has no media plane, so this room is text only.'));
    }
    if (!this.streamID) {
      return Promise.reject(new Error('The room connection is not ready yet.'));
    }

    return global.ForgeAudioInput.open(deviceId).then(function (stream) {
      self.mic = stream;
      var settings = global.ForgeAudioInput.applied(stream);
      self.on('mic', { settings: settings, concerns: global.ForgeAudioInput.concerns(settings) });

      var pc = new RTC({});
      self.pc = pc;

      pc.ontrack = function (ev) { self.attach(ev); };
      pc.onconnectionstatechange = function () {
        self.on('media', { state: pc.connectionState });
      };
      stream.getAudioTracks().forEach(function (t) { pc.addTrack(t, stream); });

      return pc.createOffer()
        .then(function (offer) { return pc.setLocalDescription(offer); })
        .then(function () { return gathered(pc); })
        .then(function () {
          return fetch('/v1/rooms/' + encodeURIComponent(self.roomID) + '/media/offer', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ stream_id: self.streamID, sdp: pc.localDescription.sdp })
          });
        })
        .then(readOrThrow)
        .then(function (answer) {
          return pc.setRemoteDescription({ type: 'answer', sdp: answer.sdp });
        })
        .then(function () { self.on('joined', {}); });
    });
  };

  /* answerOffer replies to a renegotiation the SERVER started.
   *
   * The server re-offers whenever the set of tracks this peer should receive
   * changes — every join and every departure. Answering is not optional: an
   * unanswered offer leaves the new participant permanently inaudible to this
   * browser, with nothing anywhere saying so.
   */
  Room.prototype.answerOffer = function (sdp) {
    var self = this;
    if (!this.pc) return; // not carrying audio; nothing to renegotiate
    this.pc.setRemoteDescription({ type: 'offer', sdp: sdp })
      .then(function () { return self.pc.createAnswer(); })
      .then(function (answer) { return self.pc.setLocalDescription(answer); })
      .then(function () { return gathered(self.pc); })
      .then(function () {
        return fetch('/v1/rooms/' + encodeURIComponent(self.roomID) + '/media/answer', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ stream_id: self.streamID, sdp: self.pc.localDescription.sdp })
        });
      })
      .catch(function (err) { self.on('error', { message: 'renegotiation failed: ' + err.message }); });
  };

  /* attach plays one participant's audio.
   *
   * The track names its source: `track.id` is the connection and `streamId` is
   * the person. Both come from the transport, so who is speaking is read rather
   * than guessed — which is what makes AUD-03's speaker separation real on this
   * side too.
   */
  Room.prototype.attach = function (ev) {
    var track = ev.track;
    var source = track.id;
    if (this.remote[source]) return;

    var el = new Audio();
    el.autoplay = true;
    el.srcObject = ev.streams[0] || new MediaStream([track]);
    // Kept out of the document: an <audio> element with controls would offer a
    // pause button that silences one participant with no indication to anybody,
    // including the person who pressed it.
    this.remote[source] = el;

    var self = this;
    track.onended = function () { self.detach(source); };
    el.play().catch(function () {
      // Autoplay was refused — the browser wants a gesture first. Reported so
      // the room can ask for one, rather than sitting silently while everybody
      // wonders why they cannot hear anything.
      self.on('blocked', { source: source });
    });
    this.on('remote', { source: source, user: ev.streams[0] ? ev.streams[0].id : '' });
  };

  Room.prototype.detach = function (source) {
    var el = this.remote[source];
    if (!el) return;
    el.srcObject = null;
    delete this.remote[source];
    this.on('remote-gone', { source: source });
  };

  /* ---- AUD-07 controls ---------------------------------------------------- */

  /* setState mutes, pauses or resumes.
   *
   * The local track is disabled AS WELL as telling the server, and the order
   * matters: local first, so the microphone stops feeding the encoder
   * immediately, then the server, which is what actually guarantees nobody hears
   * it. The local half is a latency optimisation on a control the server
   * enforces — not the control itself.
   */
  Room.prototype.setState = function (state) {
    var self = this;
    var silent = (state === 'muted' || state === 'paused');
    if (this.mic) {
      this.mic.getAudioTracks().forEach(function (t) { t.enabled = !silent; });
    }
    this.state = state;
    this.on('state', { state: state });

    if (!this.streamID || !this.pc) return Promise.resolve();
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID) + '/media/state', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ stream_id: this.streamID, state: state })
    }).then(readOrThrow).catch(function (err) {
      // The server did not accept it, so the mute is NOT in force however the
      // button looks. Said plainly: a control that silently failed is worse than
      // one that was never offered.
      self.on('error', { message: 'the server did not accept that change: ' + err.message });
    });
  };

  /* setTranscribing is AUD-07's end-recording(stop transcribing).
   *
   * The name differs from the requirement's on purpose. Nothing is recorded —
   * no audio is stored anywhere — so "stop recording" would promise the deletion
   * of something that was never kept. What stops is the room being written down.
   */
  Room.prototype.setTranscribing = function (on) {
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID) + '/transcribing', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ on: !!on })
    }).then(readOrThrow);
  };

  /* deleteVoice erases spoken turns. scope is 'me' or 'room'. */
  Room.prototype.deleteVoice = function (scope) {
    var self = this;
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID) +
                 '/voice?scope=' + encodeURIComponent(scope || 'me'), { method: 'DELETE' })
      .then(readOrThrow)
      .then(function (out) { self.reload(); return out; });
  };

  /* say sends a typed turn — AUD-06's non-audio path.
   *
   * Not a fallback for when the microphone fails. Every critical interaction in
   * a room has to be reachable without speaking at all, and this is it.
   */
  Room.prototype.say = function (text) {
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID) + '/turns', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text, channel: 'text' })
    }).then(readOrThrow);
  };

  /* hangUp releases the microphone and the peer connection.
   *
   * The microphone is stopped explicitly. The operating system's recording
   * indicator stays lit until every track is stopped, and one that stays on
   * after somebody left a room undoes every other privacy claim this product
   * makes.
   */
  Room.prototype.hangUp = function () {
    if (this.pc) { try { this.pc.close(); } catch (e) {} this.pc = null; }
    if (this.mic) { global.ForgeAudioInput.stop(this.mic); this.mic = null; }
    var self = this;
    Object.keys(this.remote).forEach(function (k) { self.detach(k); });
  };

  Room.prototype.leave = function () {
    this.hangUp();
    if (this.es) { this.es.close(); this.es = null; }
    return fetch('/v1/rooms/' + encodeURIComponent(this.roomID) + '/leave', { method: 'POST' });
  };

  /* ---- helpers ------------------------------------------------------------ */

  /* gathered waits for ICE gathering to finish.
   *
   * The server answers with a complete description and expects one back, so
   * there is no channel for trickled candidates and no need for one: this is a
   * connection to a server at a known address, not to a peer behind an unknown
   * NAT.
   */
  function gathered(pc) {
    if (pc.iceGatheringState === 'complete') return Promise.resolve();
    return new Promise(function (resolve) {
      function check() {
        if (pc.iceGatheringState === 'complete') {
          pc.removeEventListener('icegatheringstatechange', check);
          resolve();
        }
      }
      pc.addEventListener('icegatheringstatechange', check);
      // A ceiling, because a browser that never reports completion would
      // otherwise leave the caller waiting forever with no explanation. What has
      // been gathered by now is nearly always enough for a server on a known
      // address.
      global.setTimeout(function () {
        pc.removeEventListener('icegatheringstatechange', check);
        resolve();
      }, 3000);
    });
  }

  /* readOrThrow turns the API's error envelope into a real error.
   *
   * The server always says WHY in `error.message` and what to do in
   * `error.remedy`. Swallowing that and showing "request failed" would throw
   * away the most useful half of every failure.
   */
  function readOrThrow(resp) {
    if (resp.status === 204) return Promise.resolve({});
    return resp.json().catch(function () { return {}; }).then(function (body) {
      if (!resp.ok) {
        var e = (body && body.error) || {};
        throw new Error([e.message, e.remedy].filter(Boolean).join(' ') ||
                        ('request failed (' + resp.status + ')'));
      }
      return body;
    });
  }

  global.ForgeRoom = {
    Room: Room,
    supported: !!RTC
  };
})(window);
