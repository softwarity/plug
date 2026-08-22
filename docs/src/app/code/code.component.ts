import {
  AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  Input,
  signal,
  viewChild,
} from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import Prism from 'prismjs';
import 'prismjs/components/prism-typescript';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-markup';
import 'prismjs/components/prism-yaml';

/**
 * Renders a syntax-highlighted code block with Prism (Catppuccin Mocha theme),
 * with a one-click copy button in the top-right corner.
 *
 * Code is passed through `<ng-content>` so multi-line formatting in the source
 * template is preserved verbatim. Highlighting runs once, after the view init.
 *
 *   <app-code lang="bash">docker run ...</app-code>
 *   <app-code lang="ts">const x = 1;</app-code>
 */
@Component({
  selector: 'app-code',
  imports: [MatIconModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `<div class="wrap"><pre [class]="'language-' + lang"><code #codeEl [class]="'language-' + lang"><ng-content /></code></pre><button
        type="button"
        class="copy"
        [class.done]="copied()"
        (click)="copy()"
        [attr.aria-label]="copied() ? 'Copied' : 'Copy to clipboard'"
        title="Copy"
      ><mat-icon>{{ copied() ? 'check' : 'content_copy' }}</mat-icon></button></div>`,
  styles: [
    `
      :host {
        display: block;
        margin: 0 0 16px 0;
      }
      .wrap {
        position: relative;
      }
      .copy {
        position: absolute;
        top: 8px;
        right: 8px;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 30px;
        height: 30px;
        padding: 0;
        border: 1px solid var(--border-color);
        border-radius: 6px;
        background-color: var(--bg-secondary);
        color: var(--text-secondary);
        cursor: pointer;
        opacity: 0.55;
        transition: opacity 0.15s, color 0.15s, border-color 0.15s;
      }
      .wrap:hover .copy,
      .copy:focus-visible {
        opacity: 1;
      }
      .copy:hover {
        color: var(--text-primary);
        border-color: var(--accent-purple);
      }
      .copy.done {
        opacity: 1;
        color: #3fb950;
        border-color: #3fb950;
      }
      .copy mat-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
      }
    `,
  ],
})
export class CodeComponent implements AfterViewInit {
  @Input() lang = 'ts';

  readonly copied = signal(false);

  private readonly codeEl = viewChild.required<ElementRef<HTMLElement>>('codeEl');

  ngAfterViewInit(): void {
    Prism.highlightElement(this.codeEl().nativeElement);
  }

  copy(): void {
    const text = this.codeEl().nativeElement.textContent ?? '';
    void navigator.clipboard
      ?.writeText(text)
      .then(() => {
        this.copied.set(true);
        setTimeout(() => this.copied.set(false), 1500);
      })
      .catch(() => {
        /* clipboard blocked (e.g. denied permission) - no-op */
      });
  }
}
