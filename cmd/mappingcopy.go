package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// mappingCopyChoice is one environment whose mappings could seed a new env,
// already translated so the menu can say how many carry over.
type mappingCopyChoice struct {
	from    string
	copied  []config.DeployMapping // workspace translated to the new env
	missing []config.DeployMapping // no counterpart workspace on the new env
}

// mappingCopyChoices lists every other environment with mappings, nearest to
// alias in the customer's env order first (the one before it winning a tie),
// since TEST is a better template for PROD than DEV is.
func mappingCopyChoices(customer config.Customer, alias string) []mappingCopyChoice {
	target := findEnvIndex(customer, alias)
	if target < 0 {
		return nil
	}
	dstWorkspaces := customer.Environments[target].Workspaces

	type ranked struct {
		dist, idx int
	}
	var order []ranked
	for i, e := range customer.Environments {
		if i == target || len(e.Deployments) == 0 {
			continue
		}
		d := target - i
		if d < 0 {
			d = -d
		}
		order = append(order, ranked{d, i})
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].dist != order[b].dist {
			return order[a].dist < order[b].dist
		}
		return order[a].idx < order[b].idx
	})

	out := make([]mappingCopyChoice, 0, len(order))
	for _, r := range order {
		src := customer.Environments[r.idx]
		copied, missing := translateMappings(src.Deployments, src.Alias, alias, dstWorkspaces)
		out = append(out, mappingCopyChoice{from: src.Alias, copied: copied, missing: missing})
	}
	return out
}

// translateMappings carries mappings from env from to env to: each workspace
// name has its env token swapped ("DW - Rapporter - TEST" → "DW - Rapporter -
// PROD") and is kept only when the result is registered on the target env, so
// a copy can never point PROD at a workspace PROD doesn't own. A workspace
// without the token (a shared one) carries over unchanged under the same rule.
// Folder, repo and baseline workspace are copied as they are: the baseline is
// where git's GUIDs come from, which doesn't depend on the deploy target.
func translateMappings(src []config.DeployMapping, from, to string, dstWorkspaces []string) (copied, missing []config.DeployMapping) {
	registered := make(map[string]string, len(dstWorkspaces))
	for _, w := range dstWorkspaces {
		registered[strings.ToLower(w)] = w
	}
	for _, m := range src {
		name, ok := registered[strings.ToLower(swapEnvToken(m.Workspace, from, to))]
		if !ok {
			missing = append(missing, m)
			continue
		}
		m.Workspace = name
		copied = append(copied, m)
	}
	return copied, missing
}

// swapEnvToken replaces env alias from with to wherever it stands as a whole
// word in name, ignoring case, so "TEST" swaps in "DW - Config - TEST" but not
// in "Fabric Testing Workspace".
func swapEnvToken(name, from, to string) string {
	re := regexp.MustCompile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(from) + `($|[^\pL\pN])`)
	return re.ReplaceAllString(name, "${1}"+strings.ReplaceAll(to, "$", "$$")+"${2}")
}

// offerCopyMappings asks whether env alias should start from another env's
// mappings. done reports that the question settled the setup (the user took a
// copy), so the folder-by-folder walk is skipped; done=false with no error means
// there was nothing to copy or the user chose to map folders by hand.
// Mappings whose workspace has no counterpart on alias are asked about one by
// one, with Skip leaving them out.
func offerCopyMappings(customer config.Customer, alias string, baseOpts []ui.FilterOption, wsEnv map[string]string) (mappings []config.DeployMapping, done bool, err error) {
	choices := mappingCopyChoices(customer, alias)
	if len(choices) == 0 {
		return nil, false, nil
	}
	const byHand = "\x00byhand"

	for {
		fmt.Println()
		fmt.Println(infoStyle.Render(fmt.Sprintf("%s has no deployment mappings yet.", alias)))
		opts := make([]ui.MenuOption, 0, len(choices)+1)
		for _, c := range choices {
			total := len(c.copied) + len(c.missing)
			desc := fmt.Sprintf("%d of %d mapping(s) have a %s workspace; the rest you pick or skip.", len(c.copied), total, alias)
			if len(c.missing) == 0 {
				desc = fmt.Sprintf("All %d mapping(s) have a %s workspace.", total, alias)
			}
			opts = append(opts, ui.MenuOption{
				Label:       fmt.Sprintf("Copy %s's mappings (%d)", c.from, total),
				Value:       c.from,
				Description: desc,
			})
		}
		opts = append(opts, ui.MenuOption{
			Label:       "Map folder by folder",
			Value:       byHand,
			Description: "Pick a workspace for each repo folder yourself.",
		})
		chosen, err := ui.NumberMenu(fmt.Sprintf("Start %s's mappings from", alias), opts)
		if err != nil {
			return nil, false, err
		}
		if chosen == byHand {
			return nil, false, nil
		}

		var choice mappingCopyChoice
		for _, c := range choices {
			if c.from == chosen {
				choice = c
			}
		}
		mappings = append([]config.DeployMapping{}, choice.copied...)
		for _, m := range choice.missing {
			title := fmt.Sprintf("%s has no %s counterpart for %s — map it to?", mappingLabel(m.Folder, m.Repo), alias, m.Workspace)
			lead := []ui.FilterOption{{Label: "⋯ Skip this folder", Value: mappingSkipValue}}
			ws, err := pickMappingWorkspace(title, lead, baseOpts, wsEnv, alias)
			if err != nil {
				return nil, false, err
			}
			if ws == mappingSkipValue {
				continue
			}
			m.Workspace = ws
			mappings = append(mappings, m)
		}

		if len(mappings) == 0 {
			fmt.Println(warningStyle.Render("Nothing left to copy."))
			continue
		}
		for _, m := range mappings {
			fmt.Printf("  %s → %s\n", mappingLabel(m.Folder, m.Repo), m.Workspace)
		}
		ok, err := ui.Confirm(fmt.Sprintf("Save these %d mapping(s) on %s?", len(mappings), alias))
		if err != nil {
			return nil, false, err
		}
		if ok {
			return mappings, true, nil
		}
	}
}
