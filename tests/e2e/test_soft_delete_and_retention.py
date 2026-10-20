# file: tests/e2e/test_soft_delete_and_retention.py
# version: 1.0.1
# guid: 1c2d3e4f-5a6b-7c8d-9e0f-1a2b3c4d5e6f

import time

import pytest
from selenium.webdriver.common.by import By
from selenium.webdriver.support import expected_conditions as ec
from selenium.webdriver.support.ui import WebDriverWait


def wait_for_text(driver, text, timeout=10):
    WebDriverWait(driver, timeout).until(
        ec.presence_of_element_located((By.XPATH, f"//*[contains(., '{text}')]"))
    )


def click_first(driver, locator):
    WebDriverWait(driver, 10).until(ec.element_to_be_clickable(locator)).click()


@pytest.mark.e2e
def test_settings_retention_controls(driver, base_url):
    driver.get(f"{base_url}/settings")
    wait_for_text(driver, "Lifecycle")

    # Auto purge days input
    days_input = WebDriverWait(driver, 10).until(
        ec.presence_of_element_located(
            (By.XPATH, "//label[contains(., 'Auto-Purge After')]/following::input[1]")
        )
    )
    days_input.clear()
    days_input.send_keys("15")

    # Delete files switch
    delete_switch = driver.find_element(
        By.XPATH, "//label[contains(., 'Delete files from disk')]/span/input"
    )
    delete_switch.click()

    # Save settings
    save_btn = driver.find_element(By.XPATH, "//button[contains(., 'Save')]")
    save_btn.click()

    # Basic confirmation that values stick after a short wait
    time.sleep(0.5)
    refreshed_days = driver.find_element(
        By.XPATH, "//label[contains(., 'Auto-Purge After')]/following::input[1]"
    )
    assert refreshed_days.get_attribute("value") == "15"


@pytest.mark.e2e
def test_library_soft_deleted_section(driver, base_url):
    driver.get(f"{base_url}/library")
    wait_for_text(driver, "Library")
    wait_for_text(driver, "Soft-Deleted Books")

    # Expect either empty-state alert or a list with purge/restore buttons present
    soft_deleted_heading = driver.find_element(By.XPATH, "//h6[contains(., 'Soft-Deleted Books')]")
    assert soft_deleted_heading.is_displayed()

    # If list exists, buttons should be present
    buttons = driver.find_elements(
        By.XPATH, "//button[contains(., 'Purge now') or contains(., 'Restore')]"
    )
    if buttons:
        assert any(btn.is_displayed() for btn in buttons)
    else:
        wait_for_text(driver, "No soft-deleted books")


@pytest.mark.e2e
def test_book_detail_navigation(driver, base_url):
    driver.get(f"{base_url}/library")
    wait_for_text(driver, "Library")

    # If no books, skip gracefully
    cards = driver.find_elements(By.CSS_SELECTOR, ".MuiCard-root")
    rows = driver.find_elements(By.CSS_SELECTOR, "table tr")
    if not cards and len(rows) <= 1:  # header row plus maybe none
        pytest.skip("No audiobooks available to open detail page.")

    if cards:
        cards[0].click()
    else:
        # click first data row
        clickable = rows[1].find_element(By.CSS_SELECTOR, "td:nth-child(2)")
        clickable.click()

    wait_for_text(driver, "Back to Library")
    action_buttons = driver.find_elements(
        By.XPATH, "//button[contains(., 'Soft Delete') or contains(., 'Restore')]"
    )
    assert action_buttons, "Expected soft delete/restore controls on book detail page"
