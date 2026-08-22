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
      <div
        class="hdr"
        role="button"
        tabindex="0"
        [attr.aria-expanded]="current() !== 'collapsed'"
        (click)="cycle()"
        (keydown.enter)="cycle()"
        (keydown.space)="$event.preventDefault(); cycle()"
      >
        <mat-icon class="chev">{{ icon() }}</mat-icon>
        <span class="name">{{ name() }}</span>
        <span class="grow"></span>
        <button
          type="button"
          class="act"
          [class.done]="copied()"
          (click)="copy(); $event.stopPropagation()"
          [attr.aria-label]="copied() ? 'Copied' : 'Copy'"
          title="Copy"
        ><mat-icon>{{ copied() ? 'check' : 'content_copy' }}</mat-icon></button>
        <button
          type="button"
          class="act"
          (click)="save(); $event.stopPropagation()"
          aria-label="Download"
          title="Download"
        ><mat-icon>download</mat-icon></button>
      </div>

      @if (current() !== 'collapsed') {
        <div class="body">
          <pre [class]="'language-' + lang()"><code #codeEl [class]="'language-' + lang()"></code></pre>
          @if (current() === 'opened' && hidden() > 0) {
            <button type="button" class="more" (click)="expand()">
              <mat-icon>expand_more</mat-icon>&nbsp;{{ hidden() }} more line{{ hidden() === 1 ? '' : 's' }}
            </button>
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
        cursor: pointer;
        user-select: none;
      }
      .hdr:hover {
        background: rgba(163, 113, 247, 0.08);
      }
      .hdr:focus-visible {
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
      .grow {
        flex: 1;
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
        color: #3fb950;
        border-color: #3fb950;
      }
      .act mat-icon {
        font-size: 17px;
        width: 17px;
        height: 17px;
      }
      .body {
        border-top: 1px solid var(--border-color);
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
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((t) => this.text.set(t.replace(/\n+$/, '')))
      .catch(() => this.text.set(`# could not load ${this.src()}`));
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

  protected save(): void {
    const blob = new Blob([this.text() + '\n'], { type: 'text/yaml' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = this.name();
    a.click();
    URL.revokeObjectURL(url);
  }
}
