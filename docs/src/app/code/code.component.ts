import {
  AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  Input,
  viewChild,
} from '@angular/core';
import Prism from 'prismjs';
import 'prismjs/components/prism-typescript';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-markup';
import 'prismjs/components/prism-yaml';

/**
 * Renders a syntax-highlighted code block with Prism (Catppuccin Mocha theme).
 *
 * Code is passed through `<ng-content>` so multi-line formatting in the source
 * template is preserved verbatim. Highlighting runs once, after the view init.
 *
 *   <app-code lang="bash">docker run ...</app-code>
 *   <app-code lang="ts">const x = 1;</app-code>
 */
@Component({
  selector: 'app-code',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `<pre [class]="'language-' + lang"><code #codeEl [class]="'language-' + lang"><ng-content /></code></pre>`,
  styles: [
    `
      :host {
        display: block;
        margin: 0 0 16px 0;
      }
    `,
  ],
})
export class CodeComponent implements AfterViewInit {
  @Input() lang = 'ts';

  private readonly codeEl = viewChild.required<ElementRef<HTMLElement>>('codeEl');

  ngAfterViewInit(): void {
    Prism.highlightElement(this.codeEl().nativeElement);
  }
}
