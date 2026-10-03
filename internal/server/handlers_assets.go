package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Buildings --------------------------------------------------------------

func (s *Server) handleBuildings(w http.ResponseWriter, r *http.Request) {
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{ActiveOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "buildings/index", map[string]any{
		"Title": "Buildings", "Buildings": buildings, "Health": s.buildingHealth(tasks),
	})
}

func (s *Server) handleBuildingNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "buildings/form", map[string]any{"Title": "Add building", "Form": store.Building{}})
}

func buildingFromForm(r *http.Request, b *store.Building) []string {
	b.Name, b.Address, b.Notes = formStr(r, "name"), formStr(r, "address"), formStr(r, "notes")
	if b.Name == "" {
		return []string{"Name is required."}
	}
	return nil
}

func (s *Server) handleBuildingCreate(w http.ResponseWriter, r *http.Request) {
	var b store.Building
	if errs := buildingFromForm(r, &b); errs != nil {
		s.render(w, r, http.StatusUnprocessableEntity, "buildings/form", map[string]any{"Title": "Add building", "Form": b, "Errors": errs})
		return
	}
	if err := s.store.SaveBuilding(&b); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/buildings/%d", b.ID), b.Name+" added.")
}

func (s *Server) handleBuildingShow(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBuilding(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(b.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{BuildingID: b.ID, NoRoom: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{BuildingID: b.ID, ActiveOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	supplies, err := s.store.ListSupplies(store.SupplyFilter{BuildingID: b.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{BuildingID: b.ID, Status: "active"})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "buildings/show", map[string]any{
		"Title": b.Name, "Building": b, "Rooms": rooms, "Items": items, "Supplies": supplies,
		"Problems": problems, "Tasks": s.groupTasks(tasks), "ItemGroups": groupItems(items),
	})
}

func (s *Server) handleBuildingEdit(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBuilding(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "buildings/form", map[string]any{"Title": "Edit " + b.Name, "Form": *b})
}

func (s *Server) handleBuildingUpdate(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBuilding(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := buildingFromForm(r, b); errs != nil {
		s.render(w, r, http.StatusUnprocessableEntity, "buildings/form", map[string]any{"Title": "Edit building", "Form": *b, "Errors": errs})
		return
	}
	if err := s.store.SaveBuilding(b); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/buildings/%d", b.ID), "Building updated.")
}

func (s *Server) handleBuildingDelete(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBuilding(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteBuilding(b.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/buildings", b.Name+" and everything in it was deleted.")
}

// Rooms ------------------------------------------------------------------

func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, status int, title string, room store.Room, errs []string) {
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "rooms/form", map[string]any{"Title": title, "Form": room, "Buildings": buildings, "Errors": errs})
}

func roomFromForm(r *http.Request, room *store.Room) []string {
	room.BuildingID, room.Number, room.Name = formInt(r, "building_id"), formStr(r, "number"), formStr(r, "name")
	room.Floor, room.Notes = formStr(r, "floor"), formStr(r, "notes")
	var errs []string
	if room.BuildingID == 0 {
		errs = append(errs, "Choose a building.")
	}
	if room.Name == "" {
		errs = append(errs, "Name is required.")
	}
	return errs
}

func (s *Server) handleRoomNew(w http.ResponseWriter, r *http.Request) {
	s.roomForm(w, r, http.StatusOK, "Add room", store.Room{BuildingID: queryInt(r, "building")}, nil)
}

func (s *Server) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	var room store.Room
	if errs := roomFromForm(r, &room); errs != nil {
		s.roomForm(w, r, http.StatusUnprocessableEntity, "Add room", room, errs)
		return
	}
	if err := s.store.SaveRoom(&room); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/rooms/%d", room.ID), room.Label()+" added.")
}

// Bulk add: one room per line, as "Name" or "Number, Name" (a tab works
// too, so rows can be pasted straight from a spreadsheet).

type bulkRoomsForm struct {
	BuildingID int64
	Floor      string
	Rooms      string
}

func (s *Server) roomBulkForm(w http.ResponseWriter, r *http.Request, status int, f bulkRoomsForm, errs []string) {
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "rooms/bulk", map[string]any{"Title": "Add several rooms", "Form": f, "Buildings": buildings, "Errors": errs})
}

// parseBulkRooms turns the textarea into rooms, skipping blank lines.
func parseBulkRooms(f bulkRoomsForm) ([]store.Room, []string) {
	var rooms []store.Room
	var errs []string
	for n, line := range strings.Split(f.Rooms, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		room := store.Room{BuildingID: f.BuildingID, Floor: f.Floor, Name: line}
		if i := strings.IndexAny(line, ",\t"); i >= 0 {
			room.Number, room.Name = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		if room.Name == "" {
			errs = append(errs, fmt.Sprintf("Line %d: a room name is required.", n+1))
			continue
		}
		rooms = append(rooms, room)
	}
	if len(rooms) == 0 && len(errs) == 0 {
		errs = append(errs, "Enter at least one room.")
	}
	return rooms, errs
}

func (s *Server) handleRoomBulkNew(w http.ResponseWriter, r *http.Request) {
	s.roomBulkForm(w, r, http.StatusOK, bulkRoomsForm{BuildingID: queryInt(r, "building")}, nil)
}

func (s *Server) handleRoomBulkCreate(w http.ResponseWriter, r *http.Request) {
	f := bulkRoomsForm{BuildingID: formInt(r, "building_id"), Floor: formStr(r, "floor"), Rooms: r.PostFormValue("rooms")}
	rooms, errs := parseBulkRooms(f)
	if _, err := s.store.GetBuilding(f.BuildingID); err != nil {
		errs = append([]string{"Choose a building."}, errs...)
	}
	if errs != nil {
		s.roomBulkForm(w, r, http.StatusUnprocessableEntity, f, errs)
		return
	}
	if err := s.store.AddRooms(rooms); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/buildings/%d", f.BuildingID), pluralize(len(rooms), "room")+" added.")
}

func (s *Server) handleRoomShow(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{RoomID: room.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	supplies, err := s.store.ListSupplies(store.SupplyFilter{RoomID: room.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{RoomID: room.ID, Status: "active"})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "rooms/show", map[string]any{
		"Title": room.Label(), "Room": room, "Items": items, "ItemGroups": groupItems(items), "Supplies": supplies, "Problems": problems,
	})
}

func (s *Server) handleRoomEdit(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.roomForm(w, r, http.StatusOK, "Edit "+room.Label(), *room, nil)
}

func (s *Server) handleRoomUpdate(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := roomFromForm(r, room); errs != nil {
		s.roomForm(w, r, http.StatusUnprocessableEntity, "Edit room", *room, errs)
		return
	}
	if err := s.store.SaveRoom(room); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/rooms/%d", room.ID), "Room updated.")
}

func (s *Server) handleRoomDelete(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteRoom(room.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/buildings/%d", room.BuildingID), room.Name+" deleted. Its items and supplies are now listed under the building.")
}
