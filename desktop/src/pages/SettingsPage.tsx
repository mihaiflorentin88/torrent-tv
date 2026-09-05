import { useEffect, useState } from 'preact/hooks';
import { Settings } from '@torrent-tv/web/settings';
import { UpdateSection, type UpdateController } from '@torrent-tv/web/portal';
import { sharedApi } from '@torrent-tv/web/shared-api';
import type { Settings as SettingsRecord } from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/platform/config/models';
import type { SchemaField, SettingsView } from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/adapters/httpapi/models';
import {
  LoadSettings,
  MissingRequired,
  RestartServer,
  SaveSettings,
  SettingsSchema,
} from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings';
import { openExternal, usePortal, useServerState } from '../lib/state';

// Settings page: the shared web Settings component with the bindings as the
// PRIMARY save transport (works while the server is stopped — that is the
// point). LoadSettings/SettingsSchema also come from the bindings; only the
// Test and Maintenance tabs still talk HTTP to the live server, so a stopped
// server gets the explanatory note above the form. A save that changes
// restart-required fields surfaces the inline "Restart to apply" action; a
// save that completes the required settings auto-starts the server purely
// Go-side, and the state event flips the shell pill to running.
//
// Portal parity with web: the Account tab exists only while the snapshot
// says accounts are enabled (an outage or a disabled server unmounts the
// whole group with zero trace; the stored settings are untouched), and the
// update section renders only while THIS embedded server is running — the
// controls must never pretend a stopped (or different) server is live.
export function SettingsPage({ updates }: { updates: UpdateController }) {
  const server = useServerState();
  const portal = usePortal();
  const [value, setValue] = useState<SettingsView | null>(null);
  const [fields, setFields] = useState<SchemaField[]>([]);
  const [missing, setMissing] = useState<string[]>([]);
  const [loadError, setLoadError] = useState('');
  const [saveError, setSaveError] = useState('');
  const [restartRequired, setRestartRequired] = useState(false);
  // Bumped when a deep-link needs the shared component to remount so its
  // tab re-initializes from the URL hash.
  const [formKey, setFormKey] = useState(0);

  useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const [view, schema, missingKeys] = await Promise.all([
          LoadSettings(),
          SettingsSchema(),
          MissingRequired().catch(() => []),
        ]);
        if (!alive) return;
        setValue(view);
        setFields(schema ?? []);
        setMissing(missingKeys ?? []);
      } catch (e) {
        if (alive) setLoadError((e as Error).message);
      }
    })();
    return () => { alive = false };
  }, []);

  // Primary transport: the save bar routes the submitted body through
  // SaveSettings (Go store: restart diff + auto-start). A thrown error takes
  // the component's normal error path (onError below). onSaved then only
  // syncs the local copy of the saved values.
  async function saveTransport(out: Record<string, unknown>) {
    // The shared form emits the fields it renders; the Go side JSON-decodes
    // the payload into config.Settings, so omitted keys fall back to the
    // stored file values — the cast marks that bridge contract.
    const result = await SaveSettings(out as unknown as SettingsRecord);
    setRestartRequired(result.restartRequired);
    setMissing(await MissingRequired().catch(() => []) ?? []);
    return result;
  }

  function onSaved(saved: Record<string, unknown>) {
    setSaveError('');
    setValue(current => (current ? { ...current, ...saved } as SettingsView : current));
  }

  function focusTracker() {
    // The shared component reads its initial tab from the URL hash.
    history.replaceState(null, '', '#tracker');
    setFormKey(key => key + 1);
  }

  async function restart() {
    setSaveError('');
    try {
      await RestartServer();
      setRestartRequired(false);
    } catch (e) {
      setSaveError((e as Error).message);
    }
  }

  return (
    <section class="desktop-settings">
      {missing.length > 0 && (
        <p class="settings-status" role="alert">
          Required settings missing: {missing.join(', ')}. The server cannot start without them.
          {' '}
          <button type="button" onClick={focusTracker}>Set them in the Tracker tab</button>
        </p>
      )}
      {saveError && <p class="settings-status" role="alert">{saveError}</p>}
      {restartRequired && (
        <p class="settings-status">
          Settings saved. Restart the server to apply the changed core settings.
          {' '}
          <button class="primary" type="button" onClick={() => void restart()}>Restart to apply</button>
        </p>
      )}
      {server.state !== 'running' && (
        <p class="supporting" role="note">
          The server is {server.state}. Start the server to run tests — the Test and Maintenance tabs talk to the
          live server and show their own errors until it runs. Settings are read from disk either way.
        </p>
      )}
      {loadError
        ? <p class="settings-status" role="alert">Could not load settings: {loadError}</p>
        : value
          ? <Settings
            key={formKey}
            value={value as unknown as Record<string, unknown>}
            fields={fields}
            save={saveTransport}
            onSaved={onSaved}
            onError={message => setSaveError(message)}
            accountsEnabled={portal.snapshot?.accountsEnabled === true}
            updateSection={server.state === 'running' && portal.status
              ? <UpdateSection client={sharedApi()} status={portal.status} connected={portal.connected} failure={portal.failure} controller={updates} openExternal={openExternal} />
              : null}
          />
          : <p class="supporting">Loading settings…</p>}
    </section>
  );
}
