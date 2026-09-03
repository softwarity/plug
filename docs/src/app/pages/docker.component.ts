import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-docker',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Docker</h2>

    <p>
      Test an image <strong>in</strong> your cluster without deploying it: it reaches cluster
      services by name, and can answer to a name itself. Your image is not modified and nothing is
      rebuilt.
    </p>

    <app-code lang="bash">plug -p prod -c --dockerrun docker run --rm my-image</app-code>

    <h3>Why prefixing docker is not enough</h3>
    <p>
      <code>plug docker run my-image</code> looks like it works. plug says the tunnel is ready, the
      container starts, and nothing it does reaches the cluster. plug carries the traffic of the
      process it launches, and that process is the docker CLI: it posts a request to a socket and
      exits. The container is created by the docker <strong>daemon</strong>, which is nobody's
      child, so none of its traffic ever passes through plug.
    </p>
    <p>
      <code>--dockerrun</code> turns that around. Rather than dragging the container into plug's
      network, it puts plug in the network the container will use: a sidecar container holds the
      data path, and yours joins its network namespace. It behaves the same on macOS, Windows and
      Linux, because a container is a Linux environment on all three - VM or no VM.
    </p>

    <h3>Serving a name from a container</h3>
    <p>
      <code>-s</code> and <code>-c</code> mean here exactly what they mean everywhere else: a
      container in a cluster is a member of it like any process, so it either answers to a name or
      declares itself a pure client. Both containers share one network, so a port your image
      listens on is already reachable by the side that holds the tunnel.
    </p>
    <app-code lang="bash"># the cluster can call http://my-api:8080, served by your local image
plug -p prod -s my-api:8080:8080 --dockerrun docker run --rm my-image</app-code>

    <h3>What it does not do</h3>
    <ul>
      <li>
        <strong><code>docker run</code> only.</strong> <code>docker compose up</code>,
        <code>docker create</code> and podman build their containers differently. They are refused
        by name rather than half-supported.
      </li>
      <li>
        <strong>Foreground only.</strong> The sidecar lives as long as your command, so a
        <code>-d</code> container would outlive the network it was given.
      </li>
      <li>
        <strong>No <code>--network</code>, no <code>-p</code>, no <code>--dns</code> on your
        line.</strong> Your container shares the sidecar's network, so those belong to the sidecar.
        docker refuses them and plug explains which of your flags collided.
      </li>
    </ul>

    <h3>What it needs</h3>
    <p>
      A running docker daemon, and an image carrying the Linux plug binary - the published
      <code>softwarity/plug</code> image, by default the one matching your CLI version. Two
      environment variables cover the rest:
    </p>
    <ul>
      <li>
        <code>PLUG_DOCKER_IMAGE</code> - use another image, for instance when running a plug built
        from source whose version was never published.
      </li>
      <li>
        <code>PLUG_DOCKER_NETWORK</code> - the docker network from which your agent is reachable,
        when it is itself a container rather than a host on your network.
      </li>
    </ul>
    <p>
      This machine needs no privilege for it: nothing creates a network device here, and the
      capabilities the data path needs are granted by docker inside the sidecar. See
      <a routerLink="/security">Security model</a> for what plug does need elsewhere, and the
      <a routerLink="/cli">CLI reference</a> for the rest of the flags.
    </p>
  `,
})
export class DockerComponent {}
