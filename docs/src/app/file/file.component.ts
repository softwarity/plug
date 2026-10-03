import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  OnInit,
  computed,
  effect,
  input,
  signal,
  viewChild,
} from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import Prism from 'prismjs';
import 'prismjs/components/prism-yaml';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';

export type FileState = 'collapsed' | 'opened' | 'expanded';

/**
 * Renders a build-time-embedded asset file (e.g. a pinned deploy manifest, see
 * scripts/gen-version.mjs) with three states, cycled by clicking the header:
 *
 *   collapsed - the filename + copy/download buttons only
 *   opened - the first `preview` lines
 *   expanded - the whole file
 *
 * `states` lists which states the header cycles through, in order (default all
 * three); `initial` picks the starting one (default the first of `states`).
 * When the whole file already fits within `preview`, 'opened' and 'expanded'
 * would render identically, so 'expanded' is dropped from the cycle - no click
 * with no visible effect.
 * `maxLines` caps the visible height (opened AND expanded) to that many lines,
 * scrolling past it - omit for no cap. Highlighting reuses Prism - the same
 * highlighter and theme as <app-code> - on the fetched string, so there is no
 * second syntax-highlighting library.
 *
 *   <app-file src="assets/plug-k8s.yaml" download="plug-k8s.yaml" [preview]="14" [maxLines]="22" />
 */
@Component({
  selector: 'app-file',
  imports: [MatIconModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="file">
      <!--
        The row is a plain container, and the disclosure is a real <button> that
        wraps only what expands the file. It used to be the other way round: the
        whole row carried role="button", with Copy and Download nested INSIDE it.
        A control cannot contain controls - a screen reader announces one button
        and the two inside it are unreachable or nonsense - and aria-expanded on
        the row claimed the download button was part of the disclosure.

        A native button also brings its own keyboard handling, so the Enter and
        Space handlers that stood in for it are gone, along with the
        stopPropagation the nesting made necessary.
      -->
      <div class="hdr">
        <button
          type="button"
          class="toggle"
          [attr.aria-expanded]="current() !== 'collapsed'"
          (click)="cycle()"
        >
          <mat-icon class="chev">{{ icon() }}</mat-icon>
          <span class="name">{{ name() }}</span>
        </button>
        <!--
          Copy and Download act on the fetched text, so they are disabled until
          it is there: before the response they would copy an empty string, and
          after a failed one they used to serve the error message as if it were
          the file (a "could not load" line downloaded as plug-k8s.yaml).
        -->
        <button
          type="button"
          class="act"
          [class.done]="copied()"
          [disabled]="status() !== 'ok'"
          (click)="copy()"
          [attr.aria-label]="copied() ? 'Copied' : 'Copy'"
          title="Copy"
        ><mat-icon>{{ copied() ? 'check' : 'content_copy' }}</mat-icon></button>
        <button
          type="button"
          class="act"
          [disabled]="status() !== 'ok'"
          (click)="save()"
          aria-label="Download"
          title="Download"
        ><mat-icon>download</mat-icon></button>
      </div>

      @if (status() === 'error') {
        <div class="err" role="alert">
          <mat-icon>error_outline</mat-icon>
          <span>Could not load <code>{{ name() }}</code> ({{ error() }}): nothing to copy or download. Reload the page, or take the file from the repository.</span>
        </div>
      } @else if (current() !== 'collapsed') {
        <div class="body">
          @if (status() === 'loading') {
            <p class="loading">Loading {{ name() }}&hellip;</p>
          } @else {
            <pre [class]="'language-' + lang()"><code #codeEl [class]="'language-' + lang()"></code></pre>
            @if (current() === 'opened' && hidden() > 0) {
              <button type="button" class="more" (click)="expand()">
                <mat-icon>expand_more</mat-icon>&nbsp;{{ hidden() }} more line{{ hidden() === 1 ? '' : 's' }}
              </button>
            }
          }
        </div>
      }
    </div>
  `,
  styles: [
    `
      :host {
        display: block;
        margin: 0 0 16px 0;
      }
      .file {
        border: 1px solid var(--border-color);
        border-radius: 8px;
        overflow: hidden;
      }
      .hdr {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 10px;
        background: var(--bg-secondary);
        user-select: none;
      }
      /* The disclosure control itself: a real button, dressed as the row it used
         to be. It takes the row's width so the whole name stays clickable. */
      .toggle {
        display: flex;
        align-items: center;
        gap: 8px;
        flex: 1 1 auto;
        min-width: 0;
        padding: 0;
        border: 0;
        background: none;
        color: inherit;
        font: inherit;
        text-align: left;
        cursor: pointer;
      }
      .hdr:hover {
        background: rgba(163, 113, 247, 0.08);
      }
      .toggle:focus-visible {
        outline: 2px solid var(--accent-purple);
        outline-offset: -2px;
      }
      .chev {
        flex: none;
        color: var(--text-muted);
        font-size: 20px;
        width: 20px;
        height: 20px;
      }
      .name {
        font-family: ui-monospace, Menlo, Consolas, monospace;
        font-size: 0.85rem;
        font-weight: 600;
        color: var(--text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
      }
      .act {
        flex: none;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        border: 1px solid var(--border-color);
        border-radius: 6px;
        background: transparent;
        color: var(--text-secondary);
        cursor: pointer;
        opacity: 0.75;
        transition: opacity 0.15s, color 0.15s, border-color 0.15s;
      }
      .act:hover {
        opacity: 1;
        color: var(--text-primary);
        border-color: var(--accent-purple);
      }
      .act.done {
        opacity: 1;
        color: var(--accent-green);
        border-color: var(--accent-green);
      }
      .act:disabled {
        opacity: 0.35;
        cursor: not-allowed;
        color: var(--text-secondary);
        border-color: var(--border-color);
      }
      .act mat-icon {
        font-size: 17px;
        width: 17px;
        height: 17px;
      }
      .body {
        border-top: 1px solid var(--border-color);
      }
      .loading {
        margin: 0;
        padding: 10px 12px;
        color: var(--text-muted);
        font-size: 0.85rem;
      }
      /* The load failure, as a banner under the header: a state of the widget,
         not a line of the file. */
      .err {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 10px 12px;
        border-top: 1px solid var(--border-color);
        color: var(--text-secondary);
        font-size: 0.85rem;
      }
      .err mat-icon {
        flex: none;
        font-size: 18px;
        width: 18px;
        height: 18px;
        color: var(--accent-purple);
      }
      .body pre {
        margin: 0;
        border-radius: 0;
      }
      .more {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
        margin: 0;
        padding: 6px 12px;
        border: 0;
        border-top: 1px solid var(--border-color);
        background: var(--bg-secondary);
        color: var(--text-secondary);
        font-size: 0.8rem;
        cursor: pointer;
      }
      .more:hover {
        color: var(--text-primary);
        background: rgba(163, 113, 247, 0.08);
      }
      .more mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }
    `,
  ],
})
export class FileComponent implements OnInit {
  readonly src = input.required<string>();
  readonly lang = input('yaml');
  readonly download = input(''); // filename for the download button; default = basename(src)
  readonly preview = input(12); // lines shown in the 'opened' state
  readonly states = input<FileState[]>(['collapsed', 'opened', 'expanded']); // cycle order
  readonly initial = input<FileState | null>(null); // starting state; default = states[0]
  readonly maxLines = input<number | null>(null); // cap visible height to N lines, then scroll; null = no cap

  private readonly codeEl = viewChild<ElementRef<HTMLElement>>('codeEl');
  private readonly override = signal<FileState | null>(null);
  private readonly text = signal('');
  // The fetch's outcome. Copy and Download are enabled on 'ok' only, and the
  // body shows the file on 'ok' only: an error is reported as a banner, never
  // as the file's content.
  protected readonly status = signal<'loading' | 'ok' | 'error'>('loading');
  protected readonly error = signal('');
  protected readonly copied = signal(false);

  protected readonly current = computed<FileState>(
    () => this.override() ?? this.initial() ?? this.states()[0] ?? 'collapsed',
  );

  private readonly lines = computed(() => this.text().split('\n'));
  protected readonly hidden = computed(() => Math.max(0, this.lines().length - this.preview()));
  private readonly visible = computed(() =>
    this.current() === 'opened' ? this.lines().slice(0, this.preview()).join('\n') : this.text(),
  );

  // 'opened' and 'expanded' render identically once the whole file already
  // fits within `preview` (nothing left to reveal) - drop 'expanded' from the
  // cycle in that case, so clicking the header never produces a step with no
  // visible change. Recomputes once the file loads, so it self-corrects if the
  // file later grows past `preview`.
  private readonly effectiveStates = computed<FileState[]>(() => {
    const list = this.states();
    if (this.hidden() > 0 || !list.includes('opened') || !list.includes('expanded')) return list;
    return list.filter((s) => s !== 'expanded');
  });

  protected readonly name = computed(() => this.download() || this.src().split('/').pop() || 'file');
  protected readonly icon = computed(
    () =>
      ({ collapsed: 'chevron_right', opened: 'expand_more', expanded: 'expand_less' })[
        this.current()
      ],
  );

  constructor() {
    // Re-highlight when the visible slice (or state) changes and the <code> is in
    // the DOM. Resetting textContent clears the previous spans, then Prism
    // re-tokenises - the same path as <app-code>, no innerHTML / sanitiser.
    effect(() => {
      const el = this.codeEl()?.nativeElement;
      if (!el || this.current() === 'collapsed') return;
      el.textContent = this.visible();
      Prism.highlightElement(el);
      this.applyCap(el);
    });
  }

  // Cap the <pre> to `maxLines` (measured from the real line-height, so it is
  // exact whatever the Prism theme sets) and let it scroll past that; clear the
  // cap when maxLines is unset.
  private applyCap(code: HTMLElement): void {
    const pre = code.parentElement;
    if (!pre) return;
    const max = this.maxLines();
    if (!max || max <= 0) {
      pre.style.maxHeight = '';
      pre.style.overflowY = '';
      return;
    }
    const lh = parseFloat(getComputedStyle(code).lineHeight);
    const cs = getComputedStyle(pre);
    const pad = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
    pre.style.maxHeight = Math.round(max * (Number.isFinite(lh) ? lh : 21) + pad) + 'px';
    pre.style.overflowY = 'auto';
  }

  ngOnInit(): void {
    fetch(new URL(this.src(), document.baseURI))
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((t) => {
        this.text.set(t.replace(/\n+$/, ''));
        this.status.set('ok');
      })
      .catch((e: unknown) => {
        this.error.set(e instanceof Error && e.message ? e.message : 'network error');
        this.status.set('error');
      });
  }

  protected cycle(): void {
    const list = this.effectiveStates();
    if (list.length < 2) return;
    const i = list.indexOf(this.current());
    this.override.set(list[(i + 1) % list.length]);
  }

  protected expand(): void {
    if (this.effectiveStates().includes('expanded')) this.override.set('expanded');
  }

  protected copy(): void {
    if (this.status() !== 'ok') return; // the buttons are disabled; belt and braces
    void navigator.clipboard
      ?.writeText(this.text())
      .then(() => {
        this.copied.set(true);
        setTimeout(() => this.copied.set(false), 1500);
      })
      .catch(() => {
        /* clipboard blocked - no-op */
      });
  }

  // The MIME type of the download, from the highlighting language: the two
  // say the same thing about the file, so one input drives both.
  private mime(): string {
    const types: Record<string, string> = {
      yaml: 'text/yaml',
      json: 'application/json',
      bash: 'text/x-shellscript',
    };
    return types[this.lang()] ?? 'text/plain';
  }

  protected save(): void {
    if (this.status() !== 'ok') return;
    const blob = new Blob([this.text() + '\n'], { type: this.mime() });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = this.name();
    a.click();
    // Revoked on the next tick, not synchronously after click(): a browser that
    // starts the download asynchronously would find the URL already gone.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}
