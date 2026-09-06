// The Projects page: the server-published list of the maintainer's other
// projects, one clickable card each. Links arrive through props (the portal
// snapshot the app already holds), so the desktop shell mounts the same view
// against its own native opener without importing the web app. Remote text
// renders as text nodes — never HTML — and the full address stays visible on
// every card: it is the fallback wherever no external opener exists.
import { PortalLink } from '@torrent-tv/shared';

export function ProjectsPage({ links, openExternal }: { links: PortalLink[]; openExternal: (url: string) => void }) {
  if (!links.length) {
    return <section class="projects"><div class="projects-empty"><h2>No projects published yet</h2><p>When the server publishes other projects, they appear here with their addresses.</p></div></section>;
  }
  return <section class="projects">
    <div class="project-grid">
      {links.map(link => (
        <a class="project-card" key={link.id} href={link.url} title={link.description} rel="noreferrer noopener"
          onClick={event => { event.preventDefault(); openExternal(link.url) }}>
          <strong>{link.title}</strong>
          {link.description && <p>{link.description}</p>}
          <code>{link.url}</code>
        </a>
      ))}
    </div>
  </section>;
}
