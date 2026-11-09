import "../css/widget.css";
import { greet } from "./greet";

function Hello(props: { name: string }) {
  return <p class="hello">{greet(props.name)}</p>;
}

export const page = <Hello name="gtr" />;

export async function later() {
  const m = await import("./lazy");
  return m.lazy();
}
