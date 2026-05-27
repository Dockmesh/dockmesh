// Per-page entity context for the topbar breadcrumb. Detail pages set
// the entity name on mount; the topbar reads it to render a third
// breadcrumb segment (e.g. "fleet / containers / adminer") with the
// middle segment linked back to the list.
//
// `trail` is an optional list of intermediate crumbs inserted between
// the section/leaf and the entity name. Used when a detail page lives
// inside a logical hierarchy that doesn't match its URL (e.g. a
// container that belongs to a stack renders as
// "fleet / stacks / demo-stack / demo-container" instead of
// "fleet / containers / demo-container").

export interface TrailCrumb {
  label: string;
  href?: string;
}

function createPageContextStore() {
  let entityName = $state<string | null>(null);
  let trail = $state<TrailCrumb[]>([]);

  return {
    get entityName() {
      return entityName;
    },
    get trail() {
      return trail;
    },
    set(name: string | null, newTrail: TrailCrumb[] = []) {
      entityName = name;
      trail = newTrail;
    },
    clear() {
      entityName = null;
      trail = [];
    },
  };
}

export const pageContext = createPageContextStore();
