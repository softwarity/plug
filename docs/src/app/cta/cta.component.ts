import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { RouterLink } from '@angular/router';

/**
 * The call to action that closes a page: one filled button, an arrow, a route.
 * It was pasted into two pages with its styles and an inline `style=` on the
 * icon; the paste is now this component.
 *
 *   <app-cta link="/getting-started">Set it up</app-cta>
 */
@Component({
  selector: 'app-cta',
  imports: [MatIconModule, RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <p class="cta">
      <a [routerLink]="link()"><ng-content /> <mat-icon aria-hidden="true">arrow_forward</mat-icon></a>
    </p>
  `,
  styles: [
    `
      :host {
        display: block;
      }
      .cta {
        margin: 4px 0 8px;
      }
      .cta a {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        padding: 10px 18px;
        border-radius: 8px;
        background: var(--accent-purple);
        color: var(--text-on-accent);
        font-weight: 600;
        text-decoration: none;
      }
      .cta a:hover {
        text-decoration: none;
        filter: brightness(1.08);
      }
      .cta mat-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
      }
    `,
  ],
})
export class CtaComponent {
  readonly link = input.required<string>();
}
