import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

/** The snippets live here rather than in the template: JSON, Kotlin and TOML
 *  are full of braces, which an Angular template reads as interpolation. An
 *  interpolated string is plain text, so <app-code> gets it verbatim. */
const npm = `{
  "scripts": {
    "start:dev": "nest start --watch",
    "dev:cluster": "plug -s my-api:8080:3000 npm run start:dev"
  }
}`;

const maven = `<plugin>
  <groupId>org.codehaus.mojo</groupId>
  <artifactId>exec-maven-plugin</artifactId>
  <version>3.5.1</version>
  <executions>
    <execution>
      <id>cluster</id>
      <goals><goal>exec</goal></goals>
      <configuration>
        <executable>plug</executable>
        <arguments>
          <argument>-s</argument>
          <argument>my-api:8080:8080</argument>
          <argument>./mvnw</argument>
          <argument>spring-boot:run</argument>
        </arguments>
      </configuration>
    </execution>
  </executions>
</plugin>`;

const gradle = `tasks.register<Exec>("bootRunCluster") {
    group = "application"
    description = "Runs the app in place of my-api in the cluster"
    commandLine("plug", "-s", "my-api:8080:8080", "./gradlew", "bootRun")
}`;

const composer = `{
  "scripts": {
    "dev:cluster": [
      "Composer\\\\Config::disableProcessTimeout",
      "plug -s my-api:8080:8000 php -S 0.0.0.0:8000 -t public"
    ]
  }
}`;

const deno = `{
  "tasks": {
    "dev:cluster": "plug -s my-api:8080:8000 deno run -A main.ts"
  }
}`;

const pdm = `[tool.pdm.scripts]
dev-cluster = "plug -s my-api:8080:8000 uvicorn app:app --port 8000"`;

const hatch = `[tool.hatch.envs.default.scripts]
dev-cluster = "plug -s my-api:8080:8000 uvicorn app:app --port 8000"`;

const make = `dev-cluster:
	plug -s my-api:8080:8080 go run .`;

const just = `dev-cluster:
    plug -s my-api:8080:8080 cargo run`;

@Component({
  selector: 'app-build-tools',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>From your build tool</h2>

    <p>
      plug runs in front of the command that starts your service:
      <code>plug -s my-api:8080:3000 npm run start:dev</code>. Most build tools already have a
      place to keep such a command next to the project, so nobody types it twice and the whole team
      runs the same thing. Nothing to install beyond plug itself: each recipe below uses what the
      tool provides.
    </p>
    <p>
      Every recipe takes the place of <code>my-api</code>, listening locally on the port given last.
      Replace the name and the ports with yours; the
      <a routerLink="/cli">CLI reference</a> has every option a command can carry.
    </p>

    <h3>Node.js (npm, pnpm, Yarn, Bun)</h3>
    <p>A script in <code>package.json</code>:</p>
    <app-code lang="json">{{ npm }}</app-code>
    <app-code lang="bash">npm run dev:cluster</app-code>

    <h3>Maven</h3>
    <p>
      An execution of <code>exec-maven-plugin</code>, under <code>&lt;build&gt;&lt;plugins&gt;</code>.
      It runs only when you ask for it, never during a build:
    </p>
    <app-code lang="markup">{{ maven }}</app-code>
    <app-code lang="bash">mvn exec:exec&#64;cluster</app-code>
    <p>
      On Windows, write <code>mvnw.cmd</code> in place of <code>./mvnw</code>. Maven starts twice,
      once for the goal and once for the application: a second or two more at startup.
    </p>

    <h3>Gradle</h3>
    <p>A task of type <code>Exec</code> in <code>build.gradle.kts</code>:</p>
    <app-code lang="kotlin">{{ gradle }}</app-code>
    <app-code lang="bash">./gradlew bootRunCluster</app-code>
    <p>On Windows, write <code>gradlew.bat</code> in place of <code>./gradlew</code>.</p>

    <h3>PHP (Composer)</h3>
    <p>
      A script in <code>composer.json</code>. The first line matters: Composer stops any script
      after 300 seconds, a server included, unless it is told not to.
    </p>
    <app-code lang="json">{{ composer }}</app-code>
    <app-code lang="bash">composer run dev:cluster</app-code>

    <h3>Deno</h3>
    <p>A task in <code>deno.json</code>:</p>
    <app-code lang="json">{{ deno }}</app-code>
    <app-code lang="bash">deno task dev:cluster</app-code>

    <h3>Python (PDM, Hatch)</h3>
    <p>
      A script in <code>pyproject.toml</code>. It runs inside the project's environment, so the
      server it starts is the one installed there.
    </p>
    <app-code lang="toml">{{ pdm }}</app-code>
    <app-code lang="bash">pdm run dev-cluster</app-code>
    <p>With Hatch, the same line under the environment's scripts:</p>
    <app-code lang="toml">{{ hatch }}</app-code>
    <app-code lang="bash">hatch run dev-cluster</app-code>
    <p>
      uv and Poetry have no script runner of this kind: use the command itself
      (<code>plug -s my-api:8080:8000 uv run uvicorn app:app</code>), or a Makefile.
    </p>

    <h3>Everything else: Make, just, Taskfile</h3>
    <p>
      Go, Rust, .NET and most other languages start from a plain command
      (<code>go run .</code>, <code>cargo run</code>, <code>dotnet run</code>). A
      <code>Makefile</code> keeps it with the project:
    </p>
    <app-code lang="makefile">{{ make }}</app-code>
    <p>Or a <code>justfile</code>:</p>
    <app-code lang="makefile">{{ just }}</app-code>

    <h3>Running it from the IDE, and debugging</h3>
    <p>
      plug has to start your service: that is how the service receives the deployed one's
      configuration and how its traffic finds its way. A Run or Debug button that starts the program
      on its own leaves plug out. Run the recipe instead: every IDE can run an npm script, a Maven
      goal or a Gradle task from its own run configurations.
    </p>
    <p>To debug, start the service with its debugger listening, then attach the IDE to it:</p>
    <ul>
      <li>
        <strong>Node.js</strong>: add <code>--inspect</code> to the command that starts node, and
        attach to port 9229.
      </li>
      <li>
        <strong>Spring Boot with Maven</strong>: add the argument
        <code>-Dspring-boot.run.jvmArguments=-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005</code>
        after <code>spring-boot:run</code>, and attach to port 5005.
      </li>
      <li>
        <strong>Spring Boot with Gradle</strong>: add <code>"--debug-jvm"</code> after
        <code>"bootRun"</code>; the application waits for the debugger on port 5005.
      </li>
      <li>
        <strong>Python</strong>: start the server through debugpy
        (<code>python -m debugpy --listen 5678 -m uvicorn app:app</code>) and attach to port 5678.
      </li>
    </ul>
  `,
})
export class BuildToolsComponent {
  protected readonly npm = npm;
  protected readonly maven = maven;
  protected readonly gradle = gradle;
  protected readonly composer = composer;
  protected readonly deno = deno;
  protected readonly pdm = pdm;
  protected readonly hatch = hatch;
  protected readonly make = make;
  protected readonly just = just;
}
