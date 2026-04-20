package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const roomMaxMembers = 8
const roomMailboxLimit = 64

type RoomMessage struct {
	Seq  int
	Room string
	From string
	TS   time.Time
	Body string
}

type Room struct {
	Name      string
	Desc      string
	CreatedBy string
	CreatedAt time.Time
	Archived  bool // set when an assignment is unassigned or merged; blocks new posts

	seq int
	log []RoomMessage

	members map[string]bool
	mailbox map[string][]RoomMessage
	dropped map[string]int
	waiter  map[string]chan RoomMessage

	mu sync.Mutex
}

func newRoom(name, desc, createdBy string) *Room {
	now := time.Now()
	return &Room{
		Name:      name,
		Desc:      desc,
		CreatedBy: createdBy,
		CreatedAt: now,
		members:   map[string]bool{createdBy: true},
		mailbox:   make(map[string][]RoomMessage),
		dropped:   make(map[string]int),
		waiter:    make(map[string]chan RoomMessage),
	}
}

func validRoomName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func (s *State) opRooms(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}

	s.mu.Lock()
	rooms := make([]*Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	s.mu.Unlock()

	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
	out := make([]any, 0, len(rooms))
	for _, r := range rooms {
		r.mu.Lock()
		out = append(out, map[string]any{
			"name":       r.Name,
			"desc":       r.Desc,
			"members":    len(r.members),
			"messages":   r.seq,
			"archived":   r.Archived,
			"created_by": r.CreatedBy,
			"created_at": r.CreatedAt.Format(time.RFC3339),
		})
		r.mu.Unlock()
	}
	return Response{OK: true, Data: out}
}

func (s *State) opRoomCreate(req Request) Response {
	from, _ := req.Args["from"].(string)
	name, _ := req.Args["name"].(string)
	desc, _ := req.Args["desc"].(string)

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if !validRoomName(name) {
		return errorResponse(CodeError, "invalid_room_name", "use [A-Za-z0-9._-], max 64 chars", "invalid room name (use [A-Za-z0-9._-], max 64 chars)", map[string]any{"room": name})
	}

	s.mu.Lock()
	if _, exists := s.rooms[name]; exists {
		s.mu.Unlock()
		return errorResponse(CodeError, "room_already_exists", "pick a different room name", "room already exists: "+name, map[string]any{"room": name})
	}
	r := newRoom(name, desc, from)
	s.rooms[name] = r
	s.mu.Unlock()

	if s.queue != nil {
		_ = s.queue.roomUpsert(name, desc, from, r.CreatedAt)
		_ = s.queue.roomAddMember(name, from)
	}
	s.persistRoomDefinition(r)
	s.persistRoomMembers(r)

	return Response{OK: true, Data: map[string]any{
		"name":       name,
		"desc":       desc,
		"created_by": from,
	}}
}

func (s *State) opJoin(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" || room == "" {
		return errorResponse(CodeError, "missing_params", "provide both from and room", "from and room required", map[string]any{"from": from, "room": room})
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}

	r.mu.Lock()
	if r.members[from] {
		r.mu.Unlock()
		return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
	}
	if len(r.members) >= roomMaxMembers {
		r.mu.Unlock()
		return errorResponse(CodeNotFound, "room_not_found", "room may be full or inaccessible", "room not found: "+room, map[string]any{"room": room})
	}
	r.members[from] = true
	r.mu.Unlock()

	if s.queue != nil {
		_ = s.queue.roomAddMember(room, from)
	}
	s.persistRoomMembers(r)
	return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
}

func (s *State) opLeave(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" || room == "" {
		return errorResponse(CodeError, "missing_params", "provide both from and room", "from and room required", map[string]any{"from": from, "room": room})
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}

	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return errorResponse(CodeNotFound, "room_not_found", "join the room before leaving", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	delete(r.members, from)
	delete(r.mailbox, from)
	delete(r.dropped, from)
	delete(r.waiter, from)
	r.mu.Unlock()

	if s.queue != nil {
		_ = s.queue.roomRemoveMember(room, from)
	}
	s.persistRoomMembers(r)
	return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
}

func (s *State) opParticipants(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" || room == "" {
		return errorResponse(CodeError, "missing_params", "provide both from and room", "from and room required", map[string]any{"from": from, "room": room})
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}

	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return errorResponse(CodeNotFound, "room_not_found", "join the room before listing participants", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	names := make([]string, 0, len(r.members))
	for name := range r.members {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{
			"name":    name,
			"pending": len(r.mailbox[name]),
			"dropped": r.dropped[name],
		})
	}
	r.mu.Unlock()

	return Response{OK: true, Data: map[string]any{"room": room, "members": out}}
}

func (s *State) opPost(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	body, _ := req.Args["body"].(string)
	if from == "" || room == "" {
		return errorResponse(CodeError, "missing_params", "provide both from and room", "from and room required", map[string]any{"from": from, "room": room})
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}

	r.mu.Lock()
	if r.Archived {
		r.mu.Unlock()
		return errorResponse(CodeError, "room_archived", "this room is archived; no new posts allowed", "room archived: "+room, map[string]any{"room": room})
	}
	if !r.members[from] {
		r.mu.Unlock()
		return errorResponse(CodeNotFound, "room_not_found", "join the room before posting", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	r.seq++
	msg := RoomMessage{
		Seq:  r.seq,
		Room: r.Name,
		From: from,
		TS:   time.Now(),
		Body: body,
	}
	r.log = append(r.log, msg)
	// Deliver to each member: prefer room-specific waiter; else cross-channel
	// any-waiter; else bounded mailbox with drop-oldest policy. All delivery
	// happens under r.mu so per-sender FIFO is preserved across concurrent posts.
	// Safe: deliverAny takes s.mu briefly, but no code path takes s.mu→r.mu
	// while holding both, so no inversion.
	for member := range r.members {
		if member == from {
			continue
		}
		if ch, ok := r.waiter[member]; ok {
			ch <- msg
			delete(r.waiter, member)
			continue
		}
		if s.deliverAny(member, "room", r.Name, toPeerMsg(msg)) {
			continue
		}
		q := r.mailbox[member]
		if len(q) >= roomMailboxLimit {
			oldest := q[0]
			q = q[1:]
			r.dropped[member]++
			if s.queue != nil {
				_ = s.queue.mailboxDropOldest(member, "room", r.Name, oldest.Seq)
			}
		}
		q = append(q, msg)
		r.mailbox[member] = q
		if s.queue != nil {
			_ = s.queue.mailboxAppend(member, "room", r.Name, msg.Seq, msg.From, msg.TS, msg.Body)
		}
	}
	r.mu.Unlock()

	s.enqueueWrite(
		fmt.Sprintf("rooms/%s/%06d-%s.md", room, msg.Seq, safePathSegment(from)),
		renderRoomMsg(msg),
		fmt.Sprintf("room msg %d in %s from %s", msg.Seq, room, from),
	)
	return Response{OK: true, Data: map[string]any{
		"room": room,
		"seq":  msg.Seq,
	}}
}

// toPeerMsg converts a RoomMessage into the generic Message shape used by
// deliverAny / read-any. Room-specific fields are lost; the any-waiter has
// target=room name to disambiguate.
func toPeerMsg(rm RoomMessage) Message {
	return Message{
		Seq:  rm.Seq,
		From: rm.From,
		TS:   rm.TS,
		Body: rm.Body,
	}
}

// roomRead drains all pending messages for the caller from the named room.
// With timeout > 0 it blocks up to timeout for the first message to arrive,
// then drains whatever else accumulated. With timeout == 0 it returns
// immediately with whatever is there (possibly empty). On consume, the
// dropped counter resets and a "notice" entry is prepended if any messages
// were previously dropped.
func (s *State) roomRead(from, room string, timeout time.Duration) Response {
	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}
	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return errorResponse(CodeNotFound, "room_not_found", "join the room before reading", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	if q, ok := r.mailbox[from]; ok && len(q) > 0 {
		dropped := r.dropped[from]
		r.mailbox[from] = nil
		r.dropped[from] = 0
		if s.queue != nil {
			_ = s.queue.mailboxConsumeAll(from, "room", room)
		}
		r.mu.Unlock()
		return roomDrainResponse(room, q, dropped)
	}
	if timeout <= 0 {
		r.mu.Unlock()
		return Response{OK: true, Data: map[string]any{"room": room, "messages": []any{}}}
	}
	if _, exists := r.waiter[from]; exists {
		r.mu.Unlock()
		return errorResponse(CodeError, "room_read_already_pending", "wait for the existing read call to finish", "another read already pending for "+from+" on room "+room, map[string]any{"from": from, "room": room})
	}
	ch := make(chan RoomMessage, 1)
	r.waiter[from] = ch
	r.mu.Unlock()

	select {
	case first := <-ch:
		// Drain any additional messages that arrived between signal and here.
		r.mu.Lock()
		extra := r.mailbox[from]
		r.mailbox[from] = nil
		dropped := r.dropped[from]
		r.dropped[from] = 0
		if s.queue != nil {
			_ = s.queue.mailboxConsumeAll(from, "room", room)
		}
		r.mu.Unlock()
		all := append([]RoomMessage{first}, extra...)
		return roomDrainResponse(room, all, dropped)
	case <-time.After(timeout):
		r.mu.Lock()
		delete(r.waiter, from)
		r.mu.Unlock()
		return Response{OK: true, Data: map[string]any{"room": room, "messages": []any{}}}
	}
}

func roomDrainResponse(room string, msgs []RoomMessage, dropped int) Response {
	out := make([]any, 0, len(msgs)+1)
	if dropped > 0 {
		out = append(out, map[string]any{
			"seq":     0,
			"type":    "notice",
			"room":    room,
			"from":    "system",
			"ts":      time.Now().Format(time.RFC3339),
			"body":    fmt.Sprintf("you are behind, %d dropped", dropped),
			"notice":  true,
			"dropped": dropped,
		})
	}
	for _, m := range msgs {
		out = append(out, map[string]any{
			"type": "message",
			"seq":  m.Seq,
			"room": m.Room,
			"from": m.From,
			"ts":   m.TS.Format(time.RFC3339),
			"body": m.Body,
		})
	}
	return Response{OK: true, Data: map[string]any{"room": room, "messages": out}}
}

// roomPeek returns all pending messages without consuming. Does not clear
// the dropped counter.
func (s *State) roomPeek(from, room string) Response {
	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.members[from] {
		return errorResponse(CodeNotFound, "room_not_found", "join the room before peeking", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	queue := r.mailbox[from]
	out := make([]any, 0, len(queue))
	for _, m := range queue {
		out = append(out, map[string]any{
			"type": "message",
			"seq":  m.Seq,
			"room": m.Room,
			"from": m.From,
			"ts":   m.TS.Format(time.RFC3339),
			"body": m.Body,
		})
	}
	return Response{OK: true, Data: map[string]any{"room": room, "messages": out, "dropped": r.dropped[from]}}
}

// roomHistory returns the full room transcript (not just caller's mailbox)
// if the caller is a member.
func (s *State) roomHistory(from, room string, since, limit int) Response {
	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return errorResponse(CodeNotFound, "room_not_found", "use `lalia rooms` to list available rooms", "room not found: "+room, map[string]any{"room": room})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.members[from] {
		return errorResponse(CodeNotFound, "room_not_found", "join the room before reading history", "room not found: "+room, map[string]any{"from": from, "room": room})
	}
	out := make([]any, 0, len(r.log))
	for _, m := range r.log {
		if since > 0 && m.Seq <= since {
			continue
		}
		out = append(out, map[string]any{
			"seq":  m.Seq,
			"from": m.From,
			"ts":   m.TS.Format(time.RFC3339),
			"body": m.Body,
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return Response{OK: true, Data: map[string]any{"room": room, "messages": out}}
}

func (r *Room) removeMembers(names map[string]struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for name := range names {
		if r.members[name] {
			delete(r.members, name)
			delete(r.mailbox, name)
			delete(r.dropped, name)
			delete(r.waiter, name)
			changed = true
		}
	}
	return changed
}

func (s *State) persistRoomDefinition(r *Room) {
	r.mu.Lock()
	name := r.Name
	desc := r.Desc
	createdBy := r.CreatedBy
	createdAt := r.CreatedAt.Format(time.RFC3339)
	r.mu.Unlock()

	desc = strings.TrimSpace(desc)
	content := fmt.Sprintf(
		"# room %s\n\ncreated_by: %s\ncreated_at: %s\ndesc: %s\n",
		name, createdBy, createdAt, desc,
	)
	s.enqueueWrite(
		fmt.Sprintf("rooms/%s/ROOM.md", name),
		[]byte(content),
		fmt.Sprintf("room create: %s (by %s)", name, createdBy),
	)
}

func (s *State) persistRoomMembers(r *Room) {
	r.mu.Lock()
	name := r.Name
	names := make([]string, 0, len(r.members))
	for member := range r.members {
		names = append(names, member)
	}
	r.mu.Unlock()

	sort.Strings(names)
	lines := []string{
		fmt.Sprintf("# room %s members", name),
		"",
		fmt.Sprintf("count: %d", len(names)),
		"",
	}
	for _, member := range names {
		lines = append(lines, "- "+member)
	}
	lines = append(lines, "")
	s.enqueueWrite(
		fmt.Sprintf("rooms/%s/MEMBERS.md", name),
		[]byte(strings.Join(lines, "\n")),
		fmt.Sprintf("room members: %s", name),
	)
}

func renderRoomMsg(m RoomMessage) []byte {
	body := m.Body
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body += "\n"
	}
	return []byte(fmt.Sprintf(
		"---\nseq: %d\nfrom: %s\nroom: %s\nts: %s\n---\n\n%s",
		m.Seq, m.From, m.Room, m.TS.Format(time.RFC3339), body,
	))
}

// parseRoomMsgFile parses a room transcript file written by renderRoomMsg.
// Returns an error for any malformed file; callers skip errored files.
func parseRoomMsgFile(path string) (RoomMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RoomMessage{}, err
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return RoomMessage{}, fmt.Errorf("no frontmatter")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return RoomMessage{}, fmt.Errorf("frontmatter not closed")
	}
	header := rest[:end]
	body := strings.TrimPrefix(rest[end+5:], "\n")
	body = strings.TrimRight(body, "\n")

	var msg RoomMessage
	for _, line := range strings.Split(header, "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "seq":
			n, err := strconv.Atoi(v)
			if err != nil {
				return RoomMessage{}, fmt.Errorf("bad seq: %w", err)
			}
			msg.Seq = n
		case "from":
			msg.From = v
		case "room":
			msg.Room = v
		case "ts":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return RoomMessage{}, fmt.Errorf("bad ts: %w", err)
			}
			msg.TS = t
		}
	}
	if msg.Seq == 0 || msg.From == "" || msg.Room == "" {
		return RoomMessage{}, fmt.Errorf("incomplete frontmatter")
	}
	msg.Body = body
	return msg, nil
}

func safePathSegment(name string) string {
	if name == "" {
		return "unknown"
	}
	repl := strings.NewReplacer("/", "_", "\\", "_", " ", "_", "\t", "_", "\n", "_")
	return repl.Replace(name)
}
