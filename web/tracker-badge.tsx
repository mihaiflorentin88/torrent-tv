// Tracker source badge: renders a tracker's name as a colored pill keyed on
// the tracker id, so every surface that shows a tracker name (catalog cards,
// downloads, dialogs, player heading) reads the source consistently. Colors
// live in style.css next to the engine-tag pills; unknown tracker ids fall
// back to the neutral style, so a future tracker still renders — just gray.
export function trackerSourceKey(trackerId: string | undefined | null): string {
 const id = (trackerId || '').trim().toLowerCase();
 return id === 'filelist' || id === 'piratebay' ? id : '';
}

export function trackerBadgeClass(trackerId: string | undefined | null): string {
 const key = trackerSourceKey(trackerId);
 return key ? `tracker-badge ${key}` : 'tracker-badge';
}

export function TrackerBadge({ id, name }: { id?: string | null; name?: string | null }) {
 const label = (name || '').trim() || (id || '').trim() || 'Unknown tracker';
 return <span class={trackerBadgeClass(id)}>{label}</span>;
}
