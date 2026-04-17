package main

import (
	"fmt"
	"sort"
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

	seq int

	members map[string]bool
	mailbox map[string][]RoomMessage
	dropped map[string]int

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
		return Response{Error: "from required"}
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
		return Response{Error: "from required"}
	}
	if !validRoomName(name) {
		return Response{Error: "invalid room name (use [A-Za-z0-9._-], max 64 chars)"}
	}

	s.mu.Lock()
	if _, exists := s.rooms[name]; exists {
		s.mu.Unlock()
		return Response{Error: "room already exists: " + name}
	}
	r := newRoom(name, desc, from)
	s.rooms[name] = r
	s.mu.Unlock()

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
		return Response{Error: "from and room required"}
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}

	r.mu.Lock()
	if r.members[from] {
		r.mu.Unlock()
		return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
	}
	if len(r.members) >= roomMaxMembers {
		r.mu.Unlock()
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}
	r.members[from] = true
	r.mu.Unlock()

	s.persistRoomMembers(r)
	return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
}

func (s *State) opLeave(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" || room == "" {
		return Response{Error: "from and room required"}
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}

	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}
	delete(r.members, from)
	delete(r.mailbox, from)
	delete(r.dropped, from)
	r.mu.Unlock()

	s.persistRoomMembers(r)
	return Response{OK: true, Data: map[string]any{"room": room, "member": from}}
}

func (s *State) opParticipants(req Request) Response {
	from, _ := req.Args["from"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" || room == "" {
		return Response{Error: "from and room required"}
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}

	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
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
		return Response{Error: "from and room required"}
	}

	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}

	r.mu.Lock()
	if !r.members[from] {
		r.mu.Unlock()
		return Response{Error: "room not found: " + room, Code: CodeNotFound}
	}
	r.seq++
	msg := RoomMessage{
		Seq:  r.seq,
		Room: r.Name,
		From: from,
		TS:   time.Now(),
		Body: body,
	}
	for member := range r.members {
		if member == from {
			continue
		}
		q := r.mailbox[member]
		if len(q) >= roomMailboxLimit {
			q = q[1:]
			r.dropped[member]++
		}
		q = append(q, msg)
		r.mailbox[member] = q
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

func (s *State) opInbox(req Request) Response {
	return s.opRoomRead(req, true)
}

func (s *State) opPeek(req Request) Response {
	return s.opRoomRead(req, false)
}

func (s *State) opRoomRead(req Request, consume bool) Response {
	from, _ := req.Args["from"].(string)
	roomArg, _ := req.Args["room"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}

	if roomArg != "" {
		out, code, err := s.readRoomForMember(from, roomArg, consume)
		if err != "" {
			return Response{Error: err, Code: code}
		}
		return Response{OK: true, Data: map[string]any{"room": roomArg, "messages": out}}
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
		msgs, ok := readRoomMessages(r, from, consume)
		if !ok || len(msgs) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"room":     r.Name,
			"messages": msgs,
		})
	}
	return Response{OK: true, Data: out}
}

func (s *State) readRoomForMember(from, room string, consume bool) ([]any, int, string) {
	s.mu.Lock()
	r, ok := s.rooms[room]
	s.mu.Unlock()
	if !ok {
		return nil, CodeNotFound, "room not found: " + room
	}
	msgs, member := readRoomMessages(r, from, consume)
	if !member {
		return nil, CodeNotFound, "room not found: " + room
	}
	return msgs, 0, ""
}

func readRoomMessages(r *Room, member string, consume bool) ([]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.members[member] {
		return nil, false
	}

	queue := append([]RoomMessage(nil), r.mailbox[member]...)
	dropped := r.dropped[member]
	if consume {
		r.mailbox[member] = nil
		r.dropped[member] = 0
	}

	out := make([]any, 0, len(queue)+1)
	if consume && dropped > 0 {
		out = append(out, map[string]any{
			"seq":     0,
			"type":    "notice",
			"room":    r.Name,
			"from":    "system",
			"ts":      time.Now().Format(time.RFC3339),
			"body":    fmt.Sprintf("you are behind, %d dropped", dropped),
			"notice":  true,
			"dropped": dropped,
		})
	}
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
	return out, true
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

func safePathSegment(name string) string {
	if name == "" {
		return "unknown"
	}
	repl := strings.NewReplacer("/", "_", "\\", "_", " ", "_", "\t", "_", "\n", "_")
	return repl.Replace(name)
}
