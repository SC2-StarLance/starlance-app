package main

import "net/http"

// mapbans.go — the window's Map bans tab, a read/write view over the ladder
// API's map ban pool. The website owns the same editor; this one exists so a
// player who queues from the app never needs a browser trip to change which
// maps they refuse. Nothing here picks bans for the player: the backend still
// builds the draft and applies the pool.

// MapRef is one map of the ladder pool, as the API names it. ImageURL is the
// frontend's preview path (site-relative, e.g. /maps/previews/...webp); the
// tab resolves it against the site base so the tile shows the map itself.
type MapRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ImageURL string `json:"image_url"`
}

// MapBanView is the tab's data: the pool to choose from, the season's
// per-player allowance, and the ordered ban list saved on the account. One
// pool covers every match, regardless of either side's race.
type MapBanView struct {
	Pool      []MapRef `json:"pool"`
	Allowance int      `json:"allowance"`
	MapIDs    []string `json:"map_ids"`
}

// MapBans loads the editor's data. The pool follows the active season so a
// ban list never offers a map the draft cannot ban; without an active pool
// it falls back to the full map list, matching the website. The allowance is
// best effort (2 when the config call fails), but the ban pool is required:
// without it the editor would paint an empty account as truth.
func (s *WindowService) MapBans() (MapBanView, error) {
	out := MapBanView{Allowance: 2}

	var cfg struct {
		Draft struct {
			BansPerPlayer int `json:"bans_per_player"`
		} `json:"draft"`
	}
	if err := s.call(http.MethodGet, "/ladder/config", nil, &cfg); err == nil && cfg.Draft.BansPerPlayer > 0 {
		out.Allowance = cfg.Draft.BansPerPlayer
	}

	var seasons []struct {
		Status  string   `json:"status"`
		MapPool []MapRef `json:"map_pool"`
	}
	if err := s.call(http.MethodGet, "/seasons", nil, &seasons); err == nil {
		for _, season := range seasons {
			if season.Status == "active" && len(season.MapPool) > 0 {
				out.Pool = season.MapPool
				break
			}
		}
	}
	if out.Pool == nil {
		var maps []MapRef
		if err := s.call(http.MethodGet, "/maps", nil, &maps); err != nil {
			return out, err
		}
		out.Pool = maps
	}

	var bans struct {
		MapIDs []string `json:"map_ids"`
	}
	if err := s.call(http.MethodGet, "/me/map-bans", nil, &bans); err != nil {
		return out, err
	}
	out.MapIDs = bans.MapIDs
	return out, nil
}

// SaveMapBans replaces the account's pool with exactly what the editor holds,
// preserving its order. Nothing is guessed: the backend validates the
// allowance, and its message reaches the player unchanged.
func (s *WindowService) SaveMapBans(mapIDs []string) error {
	body := struct {
		MapIDs []string `json:"map_ids"`
	}{MapIDs: append([]string{}, mapIDs...)}
	return s.call(http.MethodPut, "/me/map-bans", body, nil)
}
