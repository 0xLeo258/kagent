import { test, expect } from "../../fixtures/test";
import { loadPage, rowNamed } from "../../helpers/app";

const SCHEDULE = "/schedules/kagent/daily-report";
const SCHEDULED_INSTANCE = "a4138a6b-3c7a-4d1e-a201-a64cbe3f72a0";

test("schedules: list, history and read-only conversation", async ({
  page,
}, testInfo) => {
  await loadPage(page, "/schedules", { title: "Schedules" });
  await expect(rowNamed(page, "daily-report")).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("schedules-list.png"),
    fullPage: true,
  });
  await page.getByRole("link", { name: "daily-report", exact: true }).click();
  await expect(page.getByTestId("page-title")).toHaveText("daily-report");
  await expect(
    page.getByText("Summarize cluster health.", { exact: true }),
  ).toBeVisible();
  await expect(rowNamed(page, "Succeeded")).toBeVisible();
  const conversation = page.getByRole("link", { name: "Open conversation" });
  await expect(conversation).toHaveAttribute(
    "href",
    `/agents/${SCHEDULED_INSTANCE}/chat`,
  );
  await page.screenshot({
    path: testInfo.outputPath("schedule-details.png"),
    fullPage: true,
  });
  await conversation.click();
  await expect(page.getByTestId("chat-input")).toBeDisabled();
  await expect(
    page.getByText("The mock cluster is healthy.", { exact: false }),
  ).toBeVisible();
  await expect(page.getByTestId("scheduled-conversation-notice")).toContainText(
    "This schedule does not allow replies or changes to its conversations.",
  );
  await expect(
      page.getByTestId("chat-share"),
    ).toHaveCount(0);
  await expect(page.locator(".ant-skeleton")).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("schedule-read-only-chat.png"),
    fullPage: true,
  });
  await page.getByTestId("chat-details").click();
  await expect(page.getByTestId("conversation-details-fields")).toBeVisible();
  await expect(page.getByTestId("conversation-details-rename")).toHaveCount(0);
});

test("schedules: pause, resume, edit and manual trigger", async ({ page }) => {
  await loadPage(page, SCHEDULE, { title: "daily-report" });
  await page.getByRole("button", { name: "Suspend", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Resume", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Trigger now", exact: true }).click();
  await expect(rowNamed(page, "Manual")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Resume", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Suspend", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Name", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("textbox", { name: "Prompt", exact: true })
    .fill("Report on deployment health.");
  await page
    .getByRole("switch", { name: "Allow conversation interaction" })
    .click();
  await page
    .getByRole("button", { name: "Save schedule", exact: true })
    .click();
  await expect(
    page.getByText("Report on deployment health.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Open conversation" }).click();
  await expect(page.getByTestId("chat-input")).toBeEnabled();
  await expect(
    page.getByTestId("chat-share"),
  ).toHaveCount(0);
});

test("schedules: create a schedule for an agent pair", async ({ page }) => {
  await loadPage(
    page,
    "/schedules/new?namespace=kagent&agentTemplate=k8s-agent-7f3a91c&harness=k8s-agent",
    { title: "New schedule" },
  );
  await page
    .getByRole("textbox", { name: "Name", exact: true })
    .fill("weekday-health");
  await page
    .getByRole("textbox", { name: "Schedule", exact: true })
    .fill("0 9 * * 1-5");
  await page
    .getByRole("textbox", { name: "Prompt", exact: true })
    .fill("Summarize deployment health every weekday.");
  await expect(
    page.getByRole("switch", { name: "Allow conversation interaction" }),
  ).not.toBeChecked();
  await page
    .getByRole("button", { name: "Create schedule", exact: true })
    .click();
  await expect(page.getByTestId("page-title")).toHaveText("weekday-health");
  await expect(page.getByText("0 9 * * 1-5", { exact: true })).toBeVisible();
  await expect(page.getByText("Read-only", { exact: true })).toBeVisible();
});
