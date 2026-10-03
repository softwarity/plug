import { Component } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { FileComponent, FileState } from './file.component';

// <app-file> fetches the asset it names; the tests replace the global fetch
// with a resolved or failed response and drive the widget through the DOM,
// the way a reader does: the header button, the "more lines" button, Copy and
// Download. The clipboard and the object URL are jsdom gaps, stubbed per test.

const SIX_LINES = ['a: 1', 'b: 2', 'c: 3', 'd: 4', 'e: 5', 'f: 6'].join('\n') + '\n';

@Component({
  imports: [FileComponent],
  template: `
    <app-file
      src="assets/sample.yaml"
      [lang]="lang"
      [download]="download"
      [preview]="preview"
      [states]="states"
      [initial]="initial"
    />
  `,
})
class HostComponent {
  lang = 'yaml';
  download = '';
  preview = 3;
  states: FileState[] = ['collapsed', 'opened', 'expanded'];
  initial: FileState | null = null;
}

function respond(body: string, init: ResponseInit = { status: 200 }) {
  return vi.fn<typeof fetch>().mockResolvedValue(new Response(body, init));
}

// The host's inputs are set BEFORE the first change detection: the fixture
// renders once with them, as a page does, instead of flipping a binding
// between two passes (which dev mode rightly reports as a changed expression).
function mount(host: Partial<HostComponent> = {}): ComponentFixture<HostComponent> {
  const fixture = TestBed.createComponent(HostComponent);
  Object.assign(fixture.componentInstance, host);
  fixture.detectChanges();
  return fixture;
}

// The fetch resolves on the microtask queue; one tick lets the status settle,
// then a change detection pass renders it.
async function settle(fixture: ComponentFixture<HostComponent>) {
  await new Promise((r) => setTimeout(r, 0));
  fixture.detectChanges();
}

function $(fixture: ComponentFixture<HostComponent>, sel: string): HTMLElement | null {
  return (fixture.nativeElement as HTMLElement).querySelector(sel);
}

function text(fixture: ComponentFixture<HostComponent>, sel: string): string {
  return $(fixture, sel)?.textContent?.trim() ?? '';
}

describe('FileComponent', () => {
  let clipboardWrite: ReturnType<typeof vi.fn>;
  let createObjectURL: ReturnType<typeof vi.fn>;
  let revokeObjectURL: ReturnType<typeof vi.fn>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({ imports: [HostComponent] }).compileComponents();
    clipboardWrite = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: clipboardWrite },
      configurable: true,
    });
    // jsdom has no object URLs at all; these two are the only URL members the
    // component touches, and they are replaced in place so `new URL()` (which
    // the fetch relies on) keeps working.
    createObjectURL = vi.fn().mockReturnValue('blob:fake');
    revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe('the fetch', () => {
    it('shows a loading line, then the file, and names it after the source', async () => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount({ initial: 'opened' });

      expect(text(fixture, '.name')).toBe('sample.yaml');
      expect(text(fixture, '.loading')).toContain('Loading sample.yaml');
      expect($(fixture, 'pre')).toBeNull();

      await settle(fixture);
      expect($(fixture, '.loading')).toBeNull();
      expect($(fixture, '.err')).toBeNull();
      expect(text(fixture, 'pre code')).toBe('a: 1\nb: 2\nc: 3');
    });

    it('resolves the source against the document base, trailing newlines trimmed', async () => {
      const fetchMock = respond(SIX_LINES + '\n\n');
      vi.stubGlobal('fetch', fetchMock);
      const fixture = mount({ initial: 'expanded' });
      await settle(fixture);

      const [url] = fetchMock.mock.calls[0];
      expect(String(url)).toBe(new URL('assets/sample.yaml', document.baseURI).href);
      expect(text(fixture, 'pre code')).toBe(SIX_LINES.trimEnd());
    });

    it('disables Copy and Download until the file is there, then enables them', async () => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount();
      const [copy, download] = Array.from(
        (fixture.nativeElement as HTMLElement).querySelectorAll<HTMLButtonElement>('.act'),
      );
      expect(copy.disabled).toBe(true);
      expect(download.disabled).toBe(true);

      await settle(fixture);
      expect(copy.disabled).toBe(false);
      expect(download.disabled).toBe(false);
    });

    it('reports an HTTP failure as a banner and keeps Copy and Download disabled', async () => {
      vi.stubGlobal('fetch', respond('nope', { status: 404 }));
      const fixture = mount({ initial: 'opened' });
      await settle(fixture);

      const err = $(fixture, '.err');
      expect(err).not.toBeNull();
      expect(err!.getAttribute('role')).toBe('alert');
      expect(err!.textContent).toContain('Could not load');
      expect(err!.textContent).toContain('sample.yaml');
      expect(err!.textContent).toContain('HTTP 404');
      // The error is never the content: no code block, nothing to copy.
      expect($(fixture, 'pre')).toBeNull();
      for (const b of (fixture.nativeElement as HTMLElement).querySelectorAll<HTMLButtonElement>('.act')) {
        expect(b.disabled).toBe(true);
      }
    });

    it('reports a network failure, and Copy and Download stay inert even when forced', async () => {
      vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockRejectedValue(new Error('offline')));
      const fixture = mount();
      await settle(fixture);

      expect(text(fixture, '.err')).toContain('(offline)');
      const [copy, download] = Array.from(
        (fixture.nativeElement as HTMLElement).querySelectorAll<HTMLButtonElement>('.act'),
      );
      // A disabled button swallows clicks; calling the handlers directly is
      // the belt-and-braces path the component guards on its own.
      copy.click();
      download.click();
      expect(clipboardWrite).not.toHaveBeenCalled();
      expect(createObjectURL).not.toHaveBeenCalled();
    });
  });

  describe('the states', () => {
    it('cycles collapsed, opened, expanded, collapsed on the header button', async () => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount();
      await settle(fixture);
      const toggle = $(fixture, '.toggle') as HTMLButtonElement;

      expect(toggle.getAttribute('aria-expanded')).toBe('false');
      expect($(fixture, '.body')).toBeNull();

      toggle.click();
      fixture.detectChanges();
      expect(toggle.getAttribute('aria-expanded')).toBe('true');
      expect(text(fixture, 'pre code')).toBe('a: 1\nb: 2\nc: 3');
      expect(text(fixture, '.more')).toContain('3 more lines');

      toggle.click();
      fixture.detectChanges();
      expect(text(fixture, 'pre code')).toBe(SIX_LINES.trimEnd());
      expect($(fixture, '.more')).toBeNull();

      toggle.click();
      fixture.detectChanges();
      expect($(fixture, '.body')).toBeNull();
    });

    it('expands from the "more lines" button, singular when one line is left', async () => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount({ initial: 'opened', preview: 5 });
      await settle(fixture);

      const more = $(fixture, '.more') as HTMLButtonElement;
      expect(more.textContent).toContain('1 more line');
      expect(more.textContent).not.toContain('lines');

      more.click();
      fixture.detectChanges();
      expect($(fixture, '.more')).toBeNull();
      expect(text(fixture, 'pre code')).toBe(SIX_LINES.trimEnd());
      expect(($(fixture, '.toggle') as HTMLButtonElement).getAttribute('aria-expanded')).toBe('true');
    });

    it('drops "expanded" from the cycle when the preview already shows the whole file', async () => {
      vi.stubGlobal('fetch', respond('one: 1\ntwo: 2\n'));
      const fixture = mount({ preview: 10 });
      await settle(fixture);
      const toggle = $(fixture, '.toggle') as HTMLButtonElement;

      toggle.click();
      fixture.detectChanges();
      expect(text(fixture, 'pre code')).toBe('one: 1\ntwo: 2');
      expect($(fixture, '.more')).toBeNull();

      // opened -> collapsed, not opened -> expanded (which would look the same)
      toggle.click();
      fixture.detectChanges();
      expect($(fixture, '.body')).toBeNull();
    });

    it('honours a restricted cycle, and a header with a single state does nothing', async () => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount({ states: ['opened'] });
      await settle(fixture);
      const toggle = $(fixture, '.toggle') as HTMLButtonElement;

      expect(text(fixture, 'pre code')).toBe('a: 1\nb: 2\nc: 3');
      toggle.click();
      fixture.detectChanges();
      expect(text(fixture, 'pre code')).toBe('a: 1\nb: 2\nc: 3');
    });
  });

  describe('copy and download', () => {
    it('copies the whole file, not the preview, and shows the check briefly', async () => {
      vi.useFakeTimers();
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const fixture = mount({ initial: 'opened' });
      await vi.advanceTimersByTimeAsync(0);
      fixture.detectChanges();

      const copy = $(fixture, '.act') as HTMLButtonElement;
      copy.click();
      await vi.advanceTimersByTimeAsync(0);
      fixture.detectChanges();

      expect(clipboardWrite).toHaveBeenCalledWith(SIX_LINES.trimEnd());
      expect(copy.classList.contains('done')).toBe(true);
      expect(copy.getAttribute('aria-label')).toBe('Copied');

      await vi.advanceTimersByTimeAsync(1500);
      fixture.detectChanges();
      expect(copy.classList.contains('done')).toBe(false);
      expect(copy.getAttribute('aria-label')).toBe('Copy');
      vi.useRealTimers();
    });

    it.each([
      ['yaml', 'text/yaml'],
      ['json', 'application/json'],
      ['bash', 'text/x-shellscript'],
      ['text', 'text/plain'],
    ])('downloads a %s file as %s, under the download name', async (lang, mime) => {
      vi.stubGlobal('fetch', respond(SIX_LINES));
      const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);

      const fixture = mount({ lang, download: `renamed.${lang}` });
      await settle(fixture);

      expect(text(fixture, '.name')).toBe(`renamed.${lang}`);
      const download = (fixture.nativeElement as HTMLElement).querySelectorAll<HTMLButtonElement>('.act')[1];
      download.click();

      expect(createObjectURL).toHaveBeenCalledTimes(1);
      const blob = createObjectURL.mock.calls[0][0] as Blob;
      expect(blob.type).toBe(mime);
      expect(await blob.text()).toBe(SIX_LINES);
      expect(click).toHaveBeenCalledTimes(1);
      const anchor = click.mock.instances[0] as HTMLAnchorElement;
      expect(anchor.download).toBe(`renamed.${lang}`);
      expect(anchor.href).toBe('blob:fake');

      // Revoked on the next tick, never synchronously after the click.
      expect(revokeObjectURL).not.toHaveBeenCalled();
      await new Promise((r) => setTimeout(r, 0));
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:fake');
    });
  });
});
